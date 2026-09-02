package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LocalUserLabel marks a Secret as a local user credential, so LocalUserStore
// can list them without needing a database.
const LocalUserLabel = "dwpk.devops-ia.io/local-user"

const (
	localUserDataUsername = "username"
	localUserDataHash     = "password-hash"
	localUserDataOwner    = "owner"
)

var (
	ErrLocalUserExists   = errors.New("local user already exists")
	ErrLocalUserNotFound = errors.New("local user not found")
	// ErrLocalUserAmbiguous is returned when one login matches more than one
	// account. That is a configuration mistake to surface, not one to resolve
	// by guessing which person was meant.
	ErrLocalUserAmbiguous = errors.New("that login matches more than one account")
)

// LocalUser is what a LocalUserStore hands back. It never carries a
// plaintext password.
type LocalUser struct {
	SecretName string
	Username   string
	Owner      string
}

// LocalUserStore persists local login credentials as Kubernetes Secrets in a
// single namespace, mirroring TokenStore's design so demo/dev deployments
// need no separate database for this optional login mode (§7.8).
type LocalUserStore struct {
	client    client.Client
	namespace string
}

func NewLocalUserStore(kubeClient client.Client, namespace string) *LocalUserStore {
	return &LocalUserStore{client: kubeClient, namespace: namespace}
}

// Create adds a new local user with a bcrypt-hashed password. Owner is the
// value matched against UserSpace.spec.owner, exactly like an OAuth2 email
// claim, so a local user reaches the same namespace/workspace a
// provider-authenticated user with the same owner value would.
func (s *LocalUserStore) Create(ctx context.Context, username, plaintextPassword, owner string) (LocalUser, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return LocalUser{}, ErrEmptyUsername
	}

	if _, err := s.findByUsername(ctx, username); err == nil {
		return LocalUser{}, fmt.Errorf("create local user %q: %w", username, ErrLocalUserExists)
	} else if !errors.Is(err, ErrLocalUserNotFound) {
		return LocalUser{}, err
	}

	hash, err := HashPassword(plaintextPassword)
	if err != nil {
		return LocalUser{}, fmt.Errorf("hash password for local user %q: %w", username, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			// Named after the person rather than generated. A Secret called
			// dwpk-local-user-jrnhg is unique and tells an operator nothing;
			// this one says whose credential it is.
			Name:      LocalUserSecretName(username),
			Namespace: s.namespace,
			Labels: map[string]string{
				LocalUserLabel: "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			localUserDataUsername: []byte(username),
			localUserDataHash:     []byte(hash),
			localUserDataOwner:    []byte(owner),
		},
	}
	if err := s.client.Create(ctx, secret); err != nil {
		return LocalUser{}, fmt.Errorf("create local user secret: %w", err)
	}

	return LocalUser{SecretName: secret.Name, Username: username, Owner: owner}, nil
}

// SetPassword replaces a local user's password, proving knowledge of the
// current one first.
//
// Requiring the current password is what makes this safe to expose on a user's
// own profile: a session hijacked long enough to load one page still cannot
// lock the owner out of their account.
func (s *LocalUserStore) SetPassword(ctx context.Context, username, currentPassword, newPassword string) error {
	secret, err := s.findByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := VerifyPassword(string(secret.Data[localUserDataHash]), currentPassword); err != nil {
		return err
	}
	return s.writePassword(ctx, secret, username, newPassword)
}

// ResetPassword sets a password without knowing the old one.
//
// This is deliberately not SetPassword with an empty current password: the
// caller must have redeemed a reset token, and that redemption is the
// authorisation. Keeping it a separate method means "no current password
// required" can never be reached by passing "" to the ordinary path.
func (s *LocalUserStore) ResetPassword(ctx context.Context, username, newPassword string) error {
	secret, err := s.findByUsername(ctx, username)
	if err != nil {
		return err
	}
	return s.writePassword(ctx, secret, username, newPassword)
}

func (s *LocalUserStore) writePassword(ctx context.Context, secret *corev1.Secret, username, newPassword string) error {
	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash new password for %q: %w", username, err)
	}

	patched := secret.DeepCopy()
	patched.Data[localUserDataHash] = []byte(hash)
	if err := s.client.Patch(ctx, patched, client.MergeFrom(secret)); err != nil {
		return fmt.Errorf("update password for %q: %w", username, err)
	}
	return nil
}

// FindByOwner returns the local users whose owner matches, so a screen can join
// credentials to the UserSpace they log into. More than one is possible:
// nothing stops two local users sharing an owner.
func (s *LocalUserStore) FindByOwner(ctx context.Context, owner string) ([]LocalUser, error) {
	users, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	matching := make([]LocalUser, 0, 1)
	for _, user := range users {
		if user.Owner == owner {
			matching = append(matching, user)
		}
	}
	return matching, nil
}

// Verify checks a login and password against the stored bcrypt hash.
//
// The login may be the username or the email. People remember one or the other,
// and refusing the one they typed teaches them nothing. Only this path accepts
// both: changing or resetting a password still resolves by username, because
// those act on one specific account rather than on whoever answers to a string.
func (s *LocalUserStore) Verify(ctx context.Context, login, plaintextPassword string) (LocalUser, error) {
	secret, err := s.findByLogin(ctx, login)
	if err != nil {
		// A login that does not exist returns immediately, while a wrong
		// password on a real account pays bcrypt's cost - a timing oracle for
		// which logins exist even though the eventual error is identical.
		// Running the same comparison against a fixed dummy hash closes it.
		_ = VerifyAgainstDummyHash(plaintextPassword)
		return LocalUser{}, err
	}

	if err := VerifyPassword(string(secret.Data[localUserDataHash]), plaintextPassword); err != nil {
		return LocalUser{}, err
	}

	return localUserFromSecret(secret), nil
}

// List returns every local user without password hashes.
func (s *LocalUserStore) List(ctx context.Context) ([]LocalUser, error) {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets,
		client.InNamespace(s.namespace),
		client.MatchingLabels{LocalUserLabel: "true"},
	); err != nil {
		return nil, fmt.Errorf("list local user secrets: %w", err)
	}

	users := make([]LocalUser, 0, len(secrets.Items))
	for i := range secrets.Items {
		users = append(users, localUserFromSecret(&secrets.Items[i]))
	}
	return users, nil
}

// Delete removes a local user by Secret name. Deleting an already-absent
// user is not an error, so callers can retry safely.
func (s *LocalUserStore) Delete(ctx context.Context, secretName string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: s.namespace}}
	if err := s.client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete local user secret %q: %w", secretName, err)
	}
	return nil
}

// findByLogin resolves a username or an owner (the email) to one account.
//
// A username match wins outright: it is the more specific identifier, and
// somebody whose username happens to be another person's email address should
// still get their own account.
//
// Two accounts answering to one login is refused rather than resolved. Choosing
// either would sign somebody in as a person they are not, and a silent
// coin-toss is a worse failure than a refusal that says what is wrong.
func (s *LocalUserStore) findByLogin(ctx context.Context, login string) (*corev1.Secret, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil, ErrLocalUserNotFound
	}

	stored, err := s.listLocalUsers(ctx)
	if err != nil {
		return nil, err
	}

	var byOwner []*corev1.Secret
	for i := range stored {
		if string(stored[i].Data[localUserDataUsername]) == login {
			return stored[i], nil
		}
		if strings.EqualFold(string(stored[i].Data[localUserDataOwner]), login) {
			byOwner = append(byOwner, stored[i])
		}
	}

	switch len(byOwner) {
	case 0:
		return nil, ErrLocalUserNotFound
	case 1:
		return byOwner[0], nil
	default:
		return nil, ErrLocalUserAmbiguous
	}
}

func (s *LocalUserStore) listLocalUsers(ctx context.Context) ([]*corev1.Secret, error) {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets,
		client.InNamespace(s.namespace),
		client.MatchingLabels{LocalUserLabel: "true"},
	); err != nil {
		return nil, fmt.Errorf("list local user secrets: %w", err)
	}
	out := make([]*corev1.Secret, 0, len(secrets.Items))
	for i := range secrets.Items {
		out = append(out, &secrets.Items[i])
	}
	return out, nil
}

func (s *LocalUserStore) findByUsername(ctx context.Context, username string) (*corev1.Secret, error) {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets,
		client.InNamespace(s.namespace),
		client.MatchingLabels{LocalUserLabel: "true"},
	); err != nil {
		return nil, fmt.Errorf("list local user secrets: %w", err)
	}

	for i := range secrets.Items {
		if string(secrets.Items[i].Data[localUserDataUsername]) == username {
			return &secrets.Items[i], nil
		}
	}
	return nil, ErrLocalUserNotFound
}

func localUserFromSecret(secret *corev1.Secret) LocalUser {
	return LocalUser{
		SecretName: secret.Name,
		Username:   string(secret.Data[localUserDataUsername]),
		Owner:      string(secret.Data[localUserDataOwner]),
	}
}
