package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestTokenMinterMint(t *testing.T) {
	t.Parallel()

	const (
		namespace          = "user-alice"
		serviceAccountName = "workspace-access"
		mintedToken        = "minted-token"
	)

	expiresAt := time.Now().Add(time.Hour).UTC().Round(time.Second)
	clientset := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: namespace}},
	)
	installCreateTokenReactor(clientset, func(action k8stesting.CreateActionImpl, req *authenticationv1.TokenRequest) (runtime.Object, error) {
		if action.Namespace != namespace {
			t.Fatalf("CreateToken() namespace = %q, want %q", action.Namespace, namespace)
		}
		if action.Name != serviceAccountName {
			t.Fatalf("CreateToken() service account = %q, want %q", action.Name, serviceAccountName)
		}
		if req.Spec.ExpirationSeconds == nil {
			t.Fatal("CreateToken() expiration seconds = nil, want value")
		}
		if got, want := *req.Spec.ExpirationSeconds, int64(3600); got != want {
			t.Fatalf("CreateToken() expiration seconds = %d, want %d", got, want)
		}

		return &authenticationv1.TokenRequest{
			Status: authenticationv1.TokenRequestStatus{
				Token:               mintedToken,
				ExpirationTimestamp: metav1.NewTime(expiresAt),
			},
		}, nil
	})

	minter := NewTokenMinter(clientset)

	token, gotExpiry, err := minter.Mint(context.Background(), namespace, serviceAccountName)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if token != mintedToken {
		t.Fatalf("Mint() token = %q, want %q", token, mintedToken)
	}
	if !gotExpiry.Equal(expiresAt) {
		t.Fatalf("Mint() expiry = %v, want %v", gotExpiry, expiresAt)
	}
}

func TestTokenMinterMintReturnsWrappedErrorForMissingServiceAccount(t *testing.T) {
	t.Parallel()

	const (
		namespace          = "user-alice"
		serviceAccountName = "missing"
	)

	clientset := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	notFoundErr := apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "serviceaccounts"}, serviceAccountName)
	installCreateTokenReactor(clientset, func(action k8stesting.CreateActionImpl, req *authenticationv1.TokenRequest) (runtime.Object, error) {
		return nil, notFoundErr
	})

	minter := NewTokenMinter(clientset)

	_, _, err := minter.Mint(context.Background(), namespace, serviceAccountName)
	if err == nil {
		t.Fatal("Mint() error = nil, want wrapped error")
	}
	if !errors.Is(err, notFoundErr) {
		t.Fatalf("Mint() error = %v, want wrapped %v", err, notFoundErr)
	}
}

func installCreateTokenReactor(clientset *fake.Clientset, reactor func(action k8stesting.CreateActionImpl, req *authenticationv1.TokenRequest) (runtime.Object, error)) {
	clientset.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}

		createAction, ok := action.(k8stesting.CreateActionImpl)
		if !ok {
			return true, nil, errors.New("unexpected action type for serviceaccounts/token")
		}

		req, ok := createAction.GetObject().(*authenticationv1.TokenRequest)
		if !ok {
			return true, nil, errors.New("unexpected object type for serviceaccounts/token")
		}

		obj, err := reactor(createAction, req)
		return true, obj, err
	})
}
