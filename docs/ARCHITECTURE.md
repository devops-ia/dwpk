# Architecture

## Overview

dwpk has three runtime components:

- `cmd/manager`, the Kubebuilder controller manager
- `cmd/gateway`, the SSH gateway
- `cmd/ui`, the marketplace web UI

The control plane state lives in Kubernetes objects. dwpk does not use an external database.

## Component map

```text
Developer browser / VS Code / ssh client
            |
            | HTTPS or SSH
            v
+------------------------------+
| dwpk UI                      |
| OAuth2 login, session store, |
| TokenRequest per request     |
+------------------------------+
            |
            | Kubernetes API with minted ServiceAccount token
            v
+------------------------------+
| kube-apiserver               |
| WorkspaceImage, UserSpace,   |
| Workspace, RBAC, webhooks    |
+------------------------------+
            ^
            |
+------------------------------+        +------------------------------+
| dwpk manager                 |        | dwpk gateway                 |
| reconciles CRDs into         |        | SSH auth, pods/exec,         |
| namespaces, quotas,          |        | pods/portforward             |
| StatefulSets, Services       |        +------------------------------+
+------------------------------+                     |
            |                                        |
            +------------------------------+---------+
                                           |
                                           v
                                user namespace, workspace pod,
                                home PVC, ServiceAccount, RBAC
```

## API model

Four CRDs define the product surface:

- `WorkspaceImage`, cluster scoped catalog entries curated by admins or synced from a registry
- `UserSpace`, cluster scoped records that map one person to one namespace and its quota, RBAC, and network policy
- `Workspace`, namespaced developer sessions backed by a single-replica `StatefulSet`
- `ImageRegistry`, cluster scoped configuration for an external registry (AWS ECR today) to poll and sync into the catalog

Relationship chain:

1. A `WorkspaceImage` publishes the runtime image, placement rules, working directory, and UID. It does not publish sizes: resources are chosen per workspace. It optionally names an `imagePullSecretRef` for a private image.
2. A `UserSpace` provisions `dwpk-<name>` and binds the owner, plus the `session` and `workspace` ServiceAccounts, to namespace-scoped access. It also mirrors every labelled pull Secret from the manager's namespace into its own, so a private catalog image can actually be pulled there.
3. A `Workspace` in that namespace references one `WorkspaceImage` by name and turns into a headless `Service`, a single-pod `StatefulSet`, and a retained home PVC.
4. An `ImageRegistry` polls its configured registry on an interval, applies include/exclude and tag selection, and owns the `WorkspaceImage` objects it creates (labelled by registry name, so a prune pass and a second registry never collide).

## Manager

`cmd/manager` wires three reconcilers and the `Workspace` admission webhooks. `ImageRegistryReconciler` is the one built around a poll timer rather than purely a watch.

### UserSpace reconciliation

`internal/controller/userspace_controller.go` turns one `UserSpace` into these children:

- Namespace `dwpk-<userspace-name>`
- `ResourceQuota` named `dwpk-quota`
- `LimitRange` named `dwpk-limits`
- `NetworkPolicy` named `dwpk-isolation`
- Three `ServiceAccount`s: `workspace`, `session`, and `session-readonly` (see below)
- Role `dwpk-workspace-user` (full owner rights) and Role `dwpk-workspace-reader` (read-only)
- RoleBindings `dwpk-owner`, `dwpk-reader`, and `dwpk-workspace-edit`
- ClusterRole `dwpk-userspace-<userspace-name>`
- ClusterRoleBinding `dwpk-userspace-<userspace-name>`

Three ServiceAccounts, not one, because a browser session and a workspace container must never
share an identity (`internal/workspace/names.go`):

- `workspace` is the identity the workspace pod itself runs as. The `dwpk-workspace-edit`
  RoleBinding grants it the built-in `edit` ClusterRole, scoped to that namespace only - this is
  what makes `kubectl` work from inside a session against the user's own namespace.
- `session` is the identity the UI mints a token for on every authenticated request
  (`TokenScopeFull`). It is bound into `dwpk-owner` alongside the human's own OIDC `User` subject,
  so a browser session gets exactly the same namespaced rights as the owner: full CRUD on
  `Workspace`, pod/log/event reads, and the `pods/exec` grant the browser terminal needs.
- `session-readonly` backs read-scoped API tokens (`TokenScopeRead`, see
  [API reference](./API_REFERENCE.md)). It is bound into `dwpk-reader`, the same shape as
  `dwpk-owner` minus every write verb. Kubernetes cannot narrow a `TokenRequest` below the
  ServiceAccount's own rights, so a read-only token has to mint for a ServiceAccount that only
  ever had read rights to begin with.

The ClusterRole exists because `UserSpace` and `WorkspaceImage` are cluster scoped. It grants:

- `get`, `watch`, and `patch` on exactly one `UserSpace`, via `resourceNames` (`patch` only so a
  person can save their own SSH keys from *My profile* - the validator refuses anyone but an
  administrator changing a field besides `spec.sshAuthorizedKeys`)
- `get`, `list`, `watch`, and the custom `use` verb on `WorkspaceImage`

Its ClusterRoleBinding names three subjects: the owner's OIDC `User`, `session`, and
`session-readonly` - all three read the catalog and their own `UserSpace` by name.

The reconciler also resolves the Kubernetes API server address from the `kubernetes` Service and its `EndpointSlice` objects, then bakes that address into the `NetworkPolicy` egress rules.

### Workspace reconciliation

`internal/controller/workspace_controller.go` drives the developer session lifecycle.

Flow:

1. Fetch the referenced `WorkspaceImage`.
2. Fail with `status.phase=Pending` and `ImageResolved=False` if the image does not exist.
3. Fail if `spec.storage` is still unset, which means the CRD default did not apply.
4. Apply a headless `Service` first, then a `StatefulSet`.
5. Reflect observed state into `status.phase`, `status.endpoint`, `status.podName`, `status.observedGeneration`, and `status.conditions`.

The controller uses a `StatefulSet`, not a `Deployment` or a bare `Pod`, because the `StatefulSet` gives stable pod naming, suspend and resume through `replicas`, and PVC retention across restarts.

`spec.running=true` maps to one replica. `spec.running=false` maps to zero replicas. The controller never deletes the home PVC - not when a workspace is suspended, and not when it is deleted. Removing it is an explicit act by the person deleting the workspace, offered in the UI's delete dialog and carried out with their own token.

### Webhooks

The manager serves two `Workspace` webhooks:

- A mutating webhook on CREATE
- A validating webhook on CREATE and UPDATE

The mutating webhook does two things, both needing an object the CRD cannot see:

- Copies the owner's `UserSpace.spec.sshAuthorizedKeys` when the workspace names none
- Stamps `metadata.annotations["dwpk.devops-ia.io/requester"]` from `request.userInfo.username`

The validating webhook makes the cross-object checks:

- `spec.imageRef.name` must resolve, and on create the entry must still be on offer
- Every SSH key must parse as a real key blob, which CEL cannot do - it can check a prefix but cannot decode base64
- `spec.volumes` must not shadow the home volume, and mounts must resolve
- Requests must not exceed limits, and an extended resource must have them equal
- The requested resources plus what is already running must fit in `UserSpace.spec.quota`, workspace count included

The quota check runs on update as well as create. It used to be skipped there,
on the reasoning that a count cannot change on an update - which stopped being
true when resources became free-form, since a resize is an update.

Rules that do not need cross-object reads stay on the CRD as CEL validations. Examples: `UserSpace.spec.owner` immutability, `Workspace.spec.storage` immutability, SSH key prefix checks, and `running=true` requiring at least one SSH key.

## SSH gateway

The gateway is a separate stateless server. It does not use leader election.

Connection flow in `internal/gateway/server.go`:

1. Accept SSH on the configured listen address.
2. List `Workspace` objects.
3. Match the SSH username to `Workspace.metadata.name`.
4. Parse each `spec.sshAuthorizedKeys` entry and compare the offered public key.
5. Store the resolved namespace and workspace name in SSH permissions.
6. Re-fetch the `Workspace` and reject the session if it is not `Running`.
7. Resolve the pod from `status.podName` or `<workspace-name>-0`.
8. Reject the target if the pod does not carry `dwpk.devops-ia.io/workspace=<workspace-name>`.
9. Open `pods/exec` for shell and exec channels.
10. Open `pods/portforward` when SSH `direct-tcpip` targets `localhost`, `127.0.0.1`, or `::1`.
11. Patch `status.lastActivityTime` on session open and close.

Why `pods/portforward` matters: VS Code Remote-SSH opens loopback listeners inside the pod. The gateway uses `pods/portforward` for `direct-tcpip` so those listeners stay reachable.

## UI

The UI is a Go server with `templ` templates, htmx, and embedded assets. It is not a SPA.

Routes from `internal/ui/server.go`:

- `GET /login`
- `GET /login/{provider}`
- `GET /callback/{provider}`
- `GET /` (dashboard)
- `GET /catalog`
- `GET /workspace-images/{name}/icon`
- `GET /new`
- `POST /new`
- `GET /w/{name}`
- `GET /w/{name}/status`
- `POST /w/{name}/start`
- `POST /w/{name}/stop`
- `GET /w/{name}/logs`
- `GET /api/v1/workspaces` and the rest of the REST surface - see `docs/API_REFERENCE.md`
- `GET /w/{name}/terminal/ws`
- `GET /admin/overview`
- `GET /admin/users`
- `GET /admin/quota`
- `GET /admin/catalog`
- `GET /admin/settings`
- `GET /admin/workspaces`
- `GET /profile`
- `POST /profile/password`
- `POST /logout`

Current screens:

- Login picker
- Dashboard: workspace status cards, counts, and quota usage - the landing page
- Catalog with text and tag filters, plus a deprecated toggle, at `/catalog`
- Workspace create form
- Workspace details page with SSH command, VS Code deep link, start and stop buttons
- Browser terminal tab backed by a websocket, with a connection chip and a reconnect button
- Logs and Events tabs, both read with the requesting user's own token
- Admin users page, joining UserSpaces to local password logins
- Admin quota page, usage against limit
- Admin catalog, workspaces, overview and settings pages
- My profile, with quota usage and a password change for local logins

The terminal is xterm.js, vendored under `internal/ui/assets/vendor` because the
CSP allows no CDN. It connects when its tab is first opened and not before: one
websocket is one `kubectl exec`, so connecting on page load started a shell in
the pod whether or not anyone wanted one.

`internal/ui/workspace.templ` splits the page in two on purpose. Only the status
card polls; the tab bar, the terminal and the log view sit outside it. They used
to be inside the polled region, so every three-second tick replaced the terminal
element - orphaning its socket, wiping the output and resetting the selected tab.
Anything that has to survive a poll belongs outside `WorkspaceStatusCard`.

Logs poll rather than stream. A second websocket would be a second transport to
secure for a tail a five-second `GET` already delivers. There is no metrics view:
it would need `metrics.k8s.io`, which dwpk does not depend on.

Destructive actions go through `confirmDelete` in `internal/ui/layout.templ` - a
native `<dialog>` holding the POST form, so the button on the page only ever
opens a question. The workspace confirmation names the retained home PVC, because
no `persistentVolumeClaimRetentionPolicy` is set on the StatefulSet
(`internal/workspace/statefulset.go`) and the volume therefore outlives the
workspace.

## Multi-provider auth model

The provider registry supports these names:

- `entra-id`
- `google`
- `gitlab`
- `keycloak`
- `github`

`cmd/ui/main.go` loads provider config from environment. OIDC providers use issuer discovery plus ID token claim checks. GitHub uses OAuth2 plus REST calls to `/user` and `/user/emails` because it is the one supported provider without an ID token flow in this codebase.

Per login:

1. User picks a provider at `/login`.
2. UI creates a short-lived login challenge with state and nonce.
3. Provider returns an authorization code to `/callback/{provider}`.
4. UI exchanges the code for tokens.
5. UI reads a verified email from the provider response.
6. UI resolves a `UserSpace` whose `spec.owner` equals that email.
7. UI creates a server-side session and stores only an opaque session ID in the browser cookie.
8. For each authenticated request, UI mints a short-lived token for the `session` ServiceAccount in the user's namespace by calling the Kubernetes `TokenRequest` API (`session-readonly` instead, for a read-scoped API token - see [API reference](./API_REFERENCE.md)).
9. UI forwards Kubernetes requests with that minted token.

This matters for security. The UI does not keep standing Kubernetes write access for user actions. Authorization still comes from the `session` ServiceAccount's own RBAC in the target namespace - a separate identity from the `workspace` ServiceAccount the pod itself runs as, so a shell inside a workspace never inherits whatever rights an administrator's own browser session carries.

## Security boundaries

Main boundaries in the current code:

- Identity comes from external OAuth2 providers.
- Namespace isolation comes from one `UserSpace` namespace per person.
- Catalog access is checked with `SelfSubjectAccessReview` for the custom `use` verb on `WorkspaceImage`.
- Session access comes from exact public key matches in `Workspace.spec.sshAuthorizedKeys`.
- Workspace API access comes from a minted `session` (or, for a read-scoped API token, `session-readonly`) ServiceAccount token scoped to the user's namespace - never the `workspace` ServiceAccount the pod itself runs as.
- Gateway pod access is derived from the `Workspace` object, never from a client-supplied pod name.
- Webhook `namespaceSelector` excludes `dwpk-system` and `kube-system` so system workloads do not deadlock behind the webhook.
- `ImageRegistry` credentials are never stored in the cluster: auth resolves through the AWS SDK's default credential chain (IRSA, EKS Pod Identity, an instance profile, or `spec.aws.roleArn` assumed via STS), all resolved inside the manager process. The one Secret this feature does touch - `imagePullSecretRef` - lives in the manager's own namespace and is mirrored per user namespace by `UserSpaceReconciler`, never read by the gateway (SPEC §6.5: the gateway never reads Secrets).

## High availability

Leader election is enabled only for the manager. `cmd/manager/main.go` sets:

- `LeaderElection=true` when `--leader-elect` is passed
- `LeaderElectionID=dwpk-controller.dwpk.devops-ia.io`
- `LeaderElectionNamespace=dwpk-system`
- Lease duration 15s
- Renew deadline 10s
- Retry period 2s
- `LeaderElectionReleaseOnCancel=true`

The raw kustomize deployment in `config/manager/manager.yaml` runs two manager replicas. The Helm chart defaults `manager.replicas` to `1`, so HA with Helm needs an explicit override.

The gateway and UI are ordinary stateless Deployments. The chart defaults are `gateway.replicas=2` and `ui.replicas=1`.

## Current implementation notes

A few design points are present in the API or spec but not finished in code:

- `Workspace.spec.idleTimeout` exists, and the gateway updates `status.lastActivityTime`, but there is no `IdleReconciler` in the repository yet. Auto-stop on idle is not implemented.
- `Workspace.spec.observability.logsEnabled` and `metricsEnabled` only stamp a pod annotation and label. The repo does not ship a workspace log sidecar injector or a workspace `PodMonitor`.
- The admin pages are routed in the UI, but the standard per-user `session` ServiceAccount does not get cluster-wide list rights. Those pages only work if an admin adds broader RBAC for that user's `session` service account.
