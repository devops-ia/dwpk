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

package v1alpha1

// EffectiveRole returns the role, substituting the default for an empty one.
func (s UserSpaceSpec) EffectiveRole() string {
	if s.Role == "" {
		return UserSpaceRoleUser
	}
	return s.Role
}

// NamespacePrefix is what an unset spec.namespace falls back to. It lives here
// rather than in the controller because the webhook, the gateway and the UI all
// have to agree on where a user's workspaces are.
const NamespacePrefix = "dwpk-"

// NamespaceName is where this user's workspaces live: their chosen namespace,
// or the generated one.
//
// Callers that have a reconciled object should prefer status.namespace, which
// is what the controller actually created. This is the desired value, and the
// two differ only while a UserSpace is being reconciled for the first time.
func (u *UserSpace) NamespaceName() string {
	if u.Spec.Namespace != "" {
		return u.Spec.Namespace
	}
	return NamespacePrefix + u.Name
}

// LoginName is what this person types to sign in: their username, or the object
// name when they have not been given one.
//
// The fallback is what makes the field additive - every UserSpace written
// before spec.username existed keeps logging in exactly as it did.
func (u *UserSpace) LoginName() string {
	if u.Spec.Username != "" {
		return u.Spec.Username
	}
	return u.Name
}

// ContactEmail is where to reach this person. It falls back to owner because
// that is what the platform used as an address before the fields were split,
// and an owner that happens to be an address is still better than nothing.
func (u *UserSpace) ContactEmail() string {
	if u.Spec.Email != "" {
		return u.Spec.Email
	}
	return u.Spec.Owner
}
