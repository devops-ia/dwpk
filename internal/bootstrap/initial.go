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

package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BootstrapSecretName holds everything an operator needs once and nothing they
// need afterwards: the generated admin password and the initial API token.
//
// One object rather than two. They had the same purpose, the same lifecycle and
// the same audience - written at install, read once, then deleted - so two of
// them meant two things to find and two things to remember to remove. What
// outlives this is stored elsewhere: the password hash in the local-user
// record, the token hash in its own.
const BootstrapSecretName = "dwpk-admin-bootstrap"

// Keys within that object.
const (
	BootstrapKeyUsername = "username"
	BootstrapKeyPassword = "password"
	BootstrapKeyToken    = "token"
)

// writeInitialValues adds keys to the bootstrap object, creating it if this is
// the first writer to arrive.
//
// The password and the token are written by different bootstrap steps that run
// independently - either may be skipped, and their order is not guaranteed - so
// neither can assume it owns the object.
//
// Existing keys are left alone. A second run must not overwrite a value the
// operator has not read yet, and re-running the bootstrap is meant to be safe.
func writeInitialValues(
	ctx context.Context,
	kubeClient client.Client,
	namespace string,
	values map[string][]byte,
) error {
	fresh := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapSecretName, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       values,
	}
	err := kubeClient.Create(ctx, fresh)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s: %w", BootstrapSecretName, err)
	}

	existing := &corev1.Secret{}
	key := client.ObjectKey{Name: BootstrapSecretName, Namespace: namespace}
	if err := kubeClient.Get(ctx, key, existing); err != nil {
		return fmt.Errorf("read %s: %w", BootstrapSecretName, err)
	}

	patched := existing.DeepCopy()
	if patched.Data == nil {
		patched.Data = map[string][]byte{}
	}
	changed := false
	for name, value := range values {
		if _, present := patched.Data[name]; present {
			continue
		}
		patched.Data[name] = value
		changed = true
	}
	if !changed {
		return nil
	}
	if err := kubeClient.Patch(ctx, patched, client.MergeFrom(existing)); err != nil {
		return fmt.Errorf("update %s: %w", BootstrapSecretName, err)
	}
	return nil
}
