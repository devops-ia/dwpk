package bootstrap

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func initialClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core types: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func readBootstrap(t *testing.T, kubeClient client.Client) map[string]string {
	t.Helper()
	stored := &corev1.Secret{}
	key := client.ObjectKey{Name: BootstrapSecretName, Namespace: "dwpk-system"}
	if err := kubeClient.Get(context.Background(), key, stored); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	values := map[string]string{}
	for name, value := range stored.Data {
		values[name] = string(value)
	}
	return values
}

// The password and the token are written by independent bootstrap steps, in no
// guaranteed order, either of which may be skipped. Whichever arrives second
// must add its key rather than replace the object.
func TestInitialValuesAccumulateFromBothWriters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubeClient := initialClient(t)

	err := writeInitialValues(ctx, kubeClient, "dwpk-system", map[string][]byte{
		BootstrapKeyUsername: []byte("admin"),
		BootstrapKeyPassword: []byte("first-value"),
	})
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	err = writeInitialValues(ctx, kubeClient, "dwpk-system", map[string][]byte{
		BootstrapKeyToken: []byte("dwpk_abc"),
	})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	values := readBootstrap(t, kubeClient)
	for name, want := range map[string]string{
		BootstrapKeyUsername: "admin",
		BootstrapKeyPassword: "first-value",
		BootstrapKeyToken:    "dwpk_abc",
	} {
		if values[name] != want {
			t.Fatalf("%s = %q, want %q - the second writer clobbered the first", name, values[name], want)
		}
	}
}

// Re-running the bootstrap must not replace a value the operator has not read
// yet. Overwriting on every restart would mean the value they wrote down stops
// working for reasons nothing explains.
func TestInitialValuesNeverOverwriteWhatIsAlreadyThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubeClient := initialClient(t)

	for _, value := range []string{"original", "regenerated"} {
		err := writeInitialValues(ctx, kubeClient, "dwpk-system", map[string][]byte{
			BootstrapKeyPassword: []byte(value),
		})
		if err != nil {
			t.Fatalf("write %q: %v", value, err)
		}
	}

	if got := readBootstrap(t, kubeClient)[BootstrapKeyPassword]; got != "original" {
		t.Fatalf("stored value = %q, want the original to survive", got)
	}
}
