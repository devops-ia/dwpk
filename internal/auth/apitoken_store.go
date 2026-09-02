package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TokenKindLabel and TokenSubjectLabels group and identify the Secrets a
// TokenStore manages, so Lookup can list-and-compare instead of needing a
// database.
const (
	TokenKindLabel         = "dwpk.devops-ia.io/token-kind"
	tokenSecretDataHash    = "hash"
	tokenSecretDataNS      = "subject-namespace"
	tokenSecretDataSA      = "subject-service-account"
	tokenSecretDataAt      = "created-at"
	tokenSecretDataExpires = "expires-at"
)

var (
	ErrTokenNotFound = errors.New("token not found")
)

// TokenRecord is what a TokenStore hands back for an issued or looked-up
// token. Plaintext is only ever populated by Issue, never by Lookup or List.
// TokenGrant is what a token is issued for. A struct rather than four
// positional strings, so a caller cannot swap the namespace and the account and
// still compile.
type TokenGrant struct {
	Kind                  TokenKind
	SubjectNamespace      string
	SubjectServiceAccount string
	// ExpiresAt is when the token stops working. The zero value means never,
	// which is a deliberate choice a person makes rather than an oversight.
	ExpiresAt time.Time
}

type TokenRecord struct {
	SecretName            string
	Kind                  TokenKind
	SubjectNamespace      string
	SubjectServiceAccount string
	CreatedAt             time.Time
	// ExpiresAt is zero for a token that never expires.
	ExpiresAt time.Time
	Plaintext string
}

// TokenStore persists API token hashes as Kubernetes Secrets in a single
// namespace, so issuing and revoking a token is nothing more than a Secret
// create/delete and needs no separate database.
type TokenStore struct {
	client    client.Client
	namespace string
}

func NewTokenStore(kubeClient client.Client, namespace string) *TokenStore {
	return &TokenStore{client: kubeClient, namespace: namespace}
}

// Issue creates a new token for the given subject ServiceAccount and
// persists only its hash. The plaintext is returned once and never stored.
func (s *TokenStore) Issue(ctx context.Context, grant TokenGrant) (TokenRecord, error) {
	kind := grant.Kind
	subjectNamespace := grant.SubjectNamespace
	subjectServiceAccount := grant.SubjectServiceAccount
	plaintext, err := GenerateAPIToken()
	if err != nil {
		return TokenRecord{}, fmt.Errorf("generate API token: %w", err)
	}

	createdAt := time.Now().UTC()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "dwpk-token-",
			Namespace:    s.namespace,
			Labels: map[string]string{
				TokenKindLabel: string(kind),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			tokenSecretDataHash: []byte(HashAPIToken(plaintext)),
			tokenSecretDataNS:   []byte(subjectNamespace),
			tokenSecretDataSA:   []byte(subjectServiceAccount),
			tokenSecretDataAt:   []byte(createdAt.Format(time.RFC3339)),
		},
	}
	// A zero expiry means never, and is stored as an absent key rather than as
	// a sentinel date: "no expiry" and "expired in 1970" must not be one value.
	if !grant.ExpiresAt.IsZero() {
		secret.Data[tokenSecretDataExpires] = []byte(grant.ExpiresAt.UTC().Format(time.RFC3339))
	}

	if err := s.client.Create(ctx, secret); err != nil {
		return TokenRecord{}, fmt.Errorf("create token secret: %w", err)
	}

	return TokenRecord{
		SecretName:            secret.Name,
		Kind:                  kind,
		SubjectNamespace:      subjectNamespace,
		SubjectServiceAccount: subjectServiceAccount,
		CreatedAt:             createdAt,
		ExpiresAt:             grant.ExpiresAt,
		Plaintext:             plaintext,
	}, nil
}

// Lookup finds the token record matching a plaintext token, or
// ErrTokenNotFound if none matches. Hash comparison runs in constant time.
func (s *TokenStore) Lookup(ctx context.Context, plaintext string) (TokenRecord, error) {
	hash := HashAPIToken(plaintext)

	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets, client.InNamespace(s.namespace)); err != nil {
		return TokenRecord{}, fmt.Errorf("list token secrets: %w", err)
	}

	for i := range secrets.Items {
		secret := &secrets.Items[i]
		kind, ok := secret.Labels[TokenKindLabel]
		if !ok {
			continue
		}
		storedHash := string(secret.Data[tokenSecretDataHash])
		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hash)) != 1 {
			continue
		}
		record := recordFromSecret(secret, TokenKind(kind))
		// Refused at lookup rather than by a sweep. A cleanup that does not run
		// - a crashed pod, a paused controller - must not leave a token working
		// past the date its owner was told it would stop.
		if record.Expired(time.Now()) {
			return TokenRecord{}, ErrTokenNotFound
		}
		return record, nil
	}

	return TokenRecord{}, ErrTokenNotFound
}

// List returns every token issued for a subject namespace, without
// plaintext values, so a user can see and revoke their own tokens.
func (s *TokenStore) List(ctx context.Context, kind TokenKind, subjectNamespace string) ([]TokenRecord, error) {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets,
		client.InNamespace(s.namespace),
		client.MatchingLabels{TokenKindLabel: string(kind)},
	); err != nil {
		return nil, fmt.Errorf("list token secrets: %w", err)
	}

	records := make([]TokenRecord, 0, len(secrets.Items))
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if subjectNamespace != "" && string(secret.Data[tokenSecretDataNS]) != subjectNamespace {
			continue
		}
		records = append(records, recordFromSecret(secret, kind))
	}
	return records, nil
}

// Revoke deletes a token by its Secret name. Deleting an already-absent
// token is not an error, so callers can retry safely.
func (s *TokenStore) Revoke(ctx context.Context, secretName string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: s.namespace}}
	if err := s.client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("revoke token secret %q: %w", secretName, err)
	}
	return nil
}

// HasAny reports whether at least one token of the given kind already
// exists, used by the admin bootstrap job to stay idempotent.
func (s *TokenStore) HasAny(ctx context.Context, kind TokenKind) (bool, error) {
	records, err := s.List(ctx, kind, "")
	if err != nil {
		return false, err
	}
	return len(records) > 0, nil
}

func recordFromSecret(secret *corev1.Secret, kind TokenKind) TokenRecord {
	createdAt, _ := time.Parse(time.RFC3339, string(secret.Data[tokenSecretDataAt]))
	// An absent key is a token that never expires. A key that will not parse is
	// treated as expired by Expired below, which is the safe direction.
	expiresAt, _ := time.Parse(time.RFC3339, string(secret.Data[tokenSecretDataExpires]))
	return TokenRecord{
		SecretName:            secret.Name,
		Kind:                  kind,
		SubjectNamespace:      string(secret.Data[tokenSecretDataNS]),
		SubjectServiceAccount: string(secret.Data[tokenSecretDataSA]),
		CreatedAt:             createdAt,
		ExpiresAt:             expiresAt,
	}
}

// Expired reports whether a token has passed its date. A record with no expiry
// never has.
func (r TokenRecord) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}
