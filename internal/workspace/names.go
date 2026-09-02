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

// Package workspace holds the pure desired-state builders for a Workspace.
package workspace

// ServiceAccountName is the ServiceAccount workspace pods run as. The
// UserSpaceReconciler creates it and binds it to `edit` in that namespace only;
// the pod builder has to name the same account or the binding buys nothing.
const ServiceAccountName = "workspace"

// SessionServiceAccountName is the identity the UI mints per-request tokens
// for. It is a second account precisely so that it is never the one a pod runs
// as: a browser session and a workspace container must not share an identity.
//
// Sharing them would mean any privilege the session needs - and an
// administrator's session needs cluster-wide rights - is also held by anyone
// with a shell in that user's workspace. Nothing may set this as a pod's
// serviceAccountName.
const SessionServiceAccountName = "session"

// ReadOnlySessionServiceAccountName is the identity a read-scoped API token
// mints for. It exists because Kubernetes cannot narrow a token below what its
// ServiceAccount holds: TokenRequest issues that account's full authority, so a
// read-only token has to be a token for a read-only account.
//
// That keeps the scope where every other decision in this platform lives - in
// RBAC, enforced by the API server. A scope checked in the UI would make the UI
// the security boundary, which is what SPEC §8.1 forbids.
const ReadOnlySessionServiceAccountName = "session-readonly"

// TokenScope is how much authority an API token carries.
//
// There are two, not three. A token can never exceed the authority of the
// person who issued it - it mints for a ServiceAccount in their own namespace,
// bound to their own rights - so an "admin" scope distinct from "full" would
// grant nothing extra to an administrator and would be unissuable to anyone
// else. Full already means "everything you can do", which for an administrator
// is everything.
type TokenScope string

const (
	// TokenScopeRead mints for the read-only ServiceAccount. A write attempted
	// with it is refused by the API server, not by us.
	TokenScopeRead TokenScope = "read"
	// TokenScopeFull mints for the session ServiceAccount: exactly the rights
	// the person themselves has, no more.
	TokenScopeFull TokenScope = "full"
)

// ServiceAccountForScope maps a scope onto the identity a token mints for.
// An unrecognised scope is read-only: the safe direction to fail in is the one
// that grants less.
func ServiceAccountForScope(scope TokenScope) string {
	if scope == TokenScopeFull {
		return SessionServiceAccountName
	}
	return ReadOnlySessionServiceAccountName
}

// TokenScopes are the choices offered, narrowest first.
func TokenScopes() []TokenScope { return []TokenScope{TokenScopeRead, TokenScopeFull} }
