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

// Package webhook holds the two admission webhooks a Workspace needs. Both
// exist for the same reason and only that reason: they read a second object,
// which CRD defaults and CEL validations cannot do (§7.2). Everything
// expressible in CEL stays on the CRD.
package webhook

import (
	"context"
	"fmt"
	"slices"

	authenticationv1 "k8s.io/api/authentication/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// SetupWorkspaceWebhookWithManager serves both Workspace webhooks off the
// manager's webhook server, using its cached client for the cross-object reads.
func SetupWorkspaceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dwpkv1alpha1.Workspace{}).
		WithDefaulter(&WorkspaceMutator{client: mgr.GetClient()}).
		WithValidator(&WorkspaceValidator{client: mgr.GetClient()}).
		Complete()
}

// CREATE only. A mutating webhook that also matched UPDATE would fight the
// controller's own writes (§7.3).
// +kubebuilder:webhook:path=/mutate-dwpk-devops-ia-io-v1alpha1-workspace,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,timeoutSeconds=5,groups=dwpk.devops-ia.io,resources=workspaces,verbs=create,versions=v1alpha1,name=mworkspace-v1alpha1.dwpk.devops-ia.io,admissionReviewVersions=v1

// WorkspaceMutator fills in the defaults that live in another object, and
// records who asked for the workspace.
type WorkspaceMutator struct {
	client client.Client
}

// Default applies defaults that depend on the referenced catalog entry and on
// who is making the request. Both are unavailable to CRD defaults (§7.3).
func (m *WorkspaceMutator) Default(ctx context.Context, ws *dwpkv1alpha1.Workspace) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("read admission request: %w", err)
	}

	stampRequester(ws, req.UserInfo)

	return m.applyOwnerDefaults(ctx, ws)
}

// applyOwnerDefaults fills in what comes from the person the workspace belongs
// to, which is now only their default SSH keys.
func (m *WorkspaceMutator) applyOwnerDefaults(ctx context.Context, ws *dwpkv1alpha1.Workspace) error {
	if len(ws.Spec.SSHAuthorizedKeys) > 0 {
		return nil
	}

	list := &dwpkv1alpha1.UserSpaceList{}
	if err := m.client.List(ctx, list); err != nil {
		return fmt.Errorf("list UserSpaces: %w", err)
	}
	for i := range list.Items {
		owner := &list.Items[i]
		if owner.Status.Namespace != ws.Namespace {
			continue
		}
		applyKeyDefault(ws, owner)
		return nil
	}
	return nil
}

// applyKeyDefault copies the owner's default keys onto a workspace that names
// none of its own.
//
// Copied, not linked. A workspace keeps the keys it was created with: adding a
// key to a profile later does not reach into running machines, and removing one
// does not silently lock somebody out of a session they are in the middle of.
// Editing the Workspace itself is how you change what it trusts.
func applyKeyDefault(ws *dwpkv1alpha1.Workspace, owner *dwpkv1alpha1.UserSpace) {
	if len(ws.Spec.SSHAuthorizedKeys) > 0 {
		return
	}
	ws.Spec.SSHAuthorizedKeys = slices.Clone(owner.Spec.SSHAuthorizedKeys)
}

// stampRequester records the authenticated username. request.userInfo is
// visible at admission and nowhere else - not to the controller, not to CRD
// defaults - so this is the only place ownership can be captured (§7.3).
func stampRequester(ws *dwpkv1alpha1.Workspace, user authenticationv1.UserInfo) {
	if user.Username == "" {
		return
	}
	if ws.Annotations == nil {
		ws.Annotations = map[string]string{}
	}
	ws.Annotations[dwpkv1alpha1.RequesterAnnotation] = user.Username
}
