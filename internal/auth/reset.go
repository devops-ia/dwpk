/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResetTokenLabel marks a Secret as a password-reset token, so the store can
// find them without knowing their names.
const ResetTokenLabel = "dwpk.devops-ia.io/password-reset"

// resetTokenTTL is how long a link stays usable. Long enough to hand over in a
// chat message or a phone call, short enough that a forgotten one expires
// before anyone finds it.
const resetTokenTTL = 24 * time.Hour

const (
	resetDataUsername = "username"
	resetDataHash     = "token-hash"
	resetDataExpires  = "expires-at"
)

var (
	// ErrResetTokenInvalid covers unknown, expired and already-used tokens
	// alike. They are one error on purpose: telling the difference tells an
	// attacker which guesses were close.
	ErrResetTokenInvalid = errors.New("password reset link is invalid or has expired")
)

// ResetStore issues and redeems single-use password reset tokens.
//
// Only the hash is stored, exactly as with API tokens: a Secret an operator can
// read must not contain something that grants access. Redeeming deletes the
// record, which is what makes the link single-use rather than merely expiring.
type ResetStore struct {
	client    client.Client
	namespace string
	now       func() time.Time
}

func NewResetStore(kubeClient client.Client, namespace string) *ResetStore {
	return &ResetStore{client: kubeClient, namespace: namespace, now: time.Now}
}

// Issue creates a reset token for a username and returns the plaintext once.
//
// Any token already outstanding for that person is removed first. Two live
// links for one account means the older one keeps working after the newer is
// used, which is not what "reset the password" is understood to mean. Expired
// records belonging to anybody go in the same pass, so they do not pile up.
func (s *ResetStore) Issue(ctx context.Context, username string) (string, time.Time, error) {
	if err := s.clearStaleAndPrevious(ctx, username); err != nil {
		return "", time.Time{}, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate reset token: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(resetTokenTTL).UTC()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "dwpk-password-reset-",
			Namespace:    s.namespace,
			Labels:       map[string]string{ResetTokenLabel: "true"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			resetDataUsername: []byte(username),
			resetDataHash:     []byte(hashToken(plaintext)),
			resetDataExpires:  []byte(expires.Format(time.RFC3339)),
		},
	}
	if err := s.client.Create(ctx, secret); err != nil {
		return "", time.Time{}, fmt.Errorf("store reset token: %w", err)
	}
	return plaintext, expires, nil
}

// Redeem consumes a token and returns whose password it resets.
//
// The record is deleted before the caller changes anything. A token that has
// been spent must not be spendable again even if the password change then
// fails: the safe failure is "ask for a new link", not "the link still works".
func (s *ResetStore) Redeem(ctx context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", ErrResetTokenInvalid
	}

	var list corev1.SecretList
	if err := s.client.List(ctx, &list,
		client.InNamespace(s.namespace),
		client.MatchingLabels{ResetTokenLabel: "true"}); err != nil {
		return "", fmt.Errorf("list reset tokens: %w", err)
	}

	wanted := hashToken(plaintext)
	for i := range list.Items {
		secret := &list.Items[i]
		// Constant time: a byte-by-byte comparison that returns early leaks how
		// much of a guess was right.
		if subtle.ConstantTimeCompare(secret.Data[resetDataHash], []byte(wanted)) != 1 {
			continue
		}

		username := string(secret.Data[resetDataUsername])
		expired := s.expired(secret)
		if err := s.client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("consume reset token: %w", err)
		}
		if expired {
			return "", ErrResetTokenInvalid
		}
		return username, nil
	}
	return "", ErrResetTokenInvalid
}

// expired treats an unparsable expiry as expired. A record we cannot read the
// deadline of is one we cannot vouch for.
func (s *ResetStore) expired(secret *corev1.Secret) bool {
	deadline, err := time.Parse(time.RFC3339, string(secret.Data[resetDataExpires]))
	if err != nil {
		return true
	}
	return s.now().After(deadline)
}

// clearStaleAndPrevious removes this person's outstanding token and, in the same
// pass, everybody's expired ones.
//
// The expired records grant nothing - Redeem refuses them regardless - but a
// namespace that only accumulates Secrets is its own problem, and issuing a link
// is the moment the list is already in hand.
func (s *ResetStore) clearStaleAndPrevious(ctx context.Context, username string) error {
	var list corev1.SecretList
	if err := s.client.List(ctx, &list,
		client.InNamespace(s.namespace),
		client.MatchingLabels{ResetTokenLabel: "true"}); err != nil {
		return fmt.Errorf("list reset tokens: %w", err)
	}
	for i := range list.Items {
		if string(list.Items[i].Data[resetDataUsername]) != username && !s.expired(&list.Items[i]) {
			continue
		}
		if err := s.client.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("replace reset token: %w", err)
		}
	}
	return nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
