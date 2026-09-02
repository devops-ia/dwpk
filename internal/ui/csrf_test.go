package ui

import "testing"

func TestCSRFStoreLifecycle(t *testing.T) {
	t.Parallel()
	store := NewCSRFStore()
	token, err := store.Ensure("session-1")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !store.Valid("session-1", token) {
		t.Fatal("token should validate")
	}
	store.Delete("session-1")
	if store.Valid("session-1", token) {
		t.Fatal("deleted token should fail")
	}
}
