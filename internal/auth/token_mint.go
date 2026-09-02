package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const serviceAccountTokenLifetime = time.Hour

var errTokenMinterClientRequired = errors.New("kubernetes clientset required")

type TokenMinter struct {
	clientset         kubernetes.Interface
	expirationSeconds int64
}

func NewTokenMinter(clientset kubernetes.Interface) *TokenMinter {
	return &TokenMinter{
		clientset:         clientset,
		expirationSeconds: int64(serviceAccountTokenLifetime / time.Second),
	}
}

func (m *TokenMinter) Mint(ctx context.Context, namespace, serviceAccountName string) (string, time.Time, error) {
	if m == nil || m.clientset == nil {
		return "", time.Time{}, fmt.Errorf("mint service account token for %s/%s: %w", namespace, serviceAccountName, errTokenMinterClientRequired)
	}

	expirationSeconds := m.expirationSeconds
	resp, err := m.clientset.CoreV1().ServiceAccounts(namespace).CreateToken(
		ctx,
		serviceAccountName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &expirationSeconds,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint service account token for %s/%s: %w", namespace, serviceAccountName, err)
	}

	return resp.Status.Token, resp.Status.ExpirationTimestamp.Time, nil
}
