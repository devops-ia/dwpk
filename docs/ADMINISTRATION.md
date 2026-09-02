# Administration

## Admin model

A cluster admin owns three jobs in dwpk:

- Curate catalog entries through `WorkspaceImage`
- Provision one `UserSpace` per person, with the quota it may spend

Users do not self-provision namespaces in the current codebase. Login succeeds only when the provider email matches an existing `UserSpace.spec.owner`.

## Manage catalog entries

`WorkspaceImage` is cluster scoped. Users read it, but do not create it.

A catalog entry controls:

- Marketplace title and description
- Container image reference and pull policy
- Working directory, pod UID, and whether root is permitted
- Placement rules
- Deprecated state, either immediately or from a date

It does not control size. Resources are chosen per workspace and checked against
the owner's quota when the workspace is admitted.

Start from the sample in `config/samples/dwpk_v1alpha1_workspaceimage.yaml`.

```sh
kubectl apply -f config/samples/dwpk_v1alpha1_workspaceimage.yaml
kubectl get workspaceimages
```

Good operating practice:

- Keep `metadata.name` stable. Users reference it through `Workspace.spec.imageRef.name`.
- Retire an entry with `spec.deprecateAt` rather than `spec.deprecated`. Until that moment the entry works normally and the catalog shows how long is left; after it, the entry behaves as deprecated. `spec.deprecated=true` is the immediate form.
- A deprecated entry keeps its existing workspaces running. It only stops being offered for new ones, and the UI still shows it when the user asks for deprecated entries.
- `spec.allowRoot` lets workspaces from that entry run as uid 0. Only an administrator can set it, and it is the one field worth a second look before saving.
- Test placement rules. `spec.placement.nodeSelector` and `spec.placement.tolerations` are passed straight into the workspace pod spec.
- A private image needs `spec.imagePullSecretRef` naming a pull Secret. dwpk replicates that Secret from the manager's own namespace into every user namespace - see "Sync the catalog from a registry" below for how the Secret gets there when it comes from a synced registry, or label one yourself with `dwpk.devops-ia.io/pull-secret=true` for a hand-added entry.

## Sync the catalog from a registry

Under Administration → Catalog, a **Registries** section configures `ImageRegistry` objects that
poll an external registry (AWS ECR today) and keep matching WorkspaceImages in sync automatically,
instead of adding entries by hand.

Each registry configures:

- **Where**: AWS region, account ID (defaults to the credentials' own account), and an optional
  cross-account role ARN. Authentication is the AWS SDK's default credential chain - IRSA, EKS Pod
  Identity, or an instance profile; no AWS key ever needs to be stored in the cluster.
- **What to sync**: include/exclude RE2 patterns matched against repository names (one per line;
  exclude wins when both match), and a tag policy - either the newest tag by push time, or every
  tag matching a set of patterns, capped at a configurable number per repository.
- **How often**: a sync interval in seconds (minimum 60), plus a **Force sync** button per registry
  to retrigger one immediately.
- **Cleanup**: an opt-in "delete on disappearance" toggle. Off by default, so a registry that is
  briefly unreachable does not delete catalog entries somebody may be using.
- **Private images**: an optional pull-secret name, stamped onto every entry the registry syncs.

Every synced entry shows which registry produced it and is labelled
`dwpk.devops-ia.io/image-registry=<registry name>`. Editing a synced entry's fields by hand is
possible but pointless - the next sync overwrites them, and the admin screen says so.

Deleting an `ImageRegistry` deletes every WorkspaceImage it synced (`ownerReferences`); workspaces
already running one of those images keep running.

## Provision UserSpaces

A `UserSpace` creates the tenant boundary for one person.

```sh
kubectl apply -f config/samples/dwpk_v1alpha1_userspace.yaml
kubectl get userspaces
```

The controller derives the namespace as `dwpk-<userspace-name>` and writes that value into `status.namespace`.

Each `UserSpace` currently provisions:

- Namespace
- ResourceQuota
- LimitRange
- NetworkPolicy
- Three ServiceAccounts: `workspace` (the pod's own identity), `session` (the UI's per-request
  full-access token), and `session-readonly` (read-scoped API tokens)
- Per-user namespaced Roles and RoleBindings, one pair for full access and one for read-only
- Per-user ClusterRole and ClusterRoleBinding for cluster-scoped reads

## RBAC that ships with the repo

Helper ClusterRoles in `config/rbac/`:

- `workspace-admin-role`
- `workspace-editor-role`
- `workspace-viewer-role`
- `userspace-admin-role`
- `userspace-editor-role`
- `userspace-viewer-role`
- `workspaceimage-admin-role`
- `workspaceimage-editor-role`
- `workspaceimage-viewer-role`
- `platformconfig-admin-role`
- `workspace-volume-admin-role`

The `-editor-role` and `-viewer-role` variants are not used by dwpk itself, as
`config/rbac/kustomization.yaml` says. The five `-admin-role` ClusterRoles are:
the manager binds them to every `UserSpace` whose role is `admin`, which is what
`--admin-cluster-roles` lists.

That binding is what authorizes an administrator's own token, and the UI talks
to the API server as the signed-in person rather than as itself. **So any admin
screen that touches a resource not already covered here needs a new role adding
to this list, `config/rbac/kustomization.yaml`, the chart, and the
`dwpk.adminClusterRoles` helper - otherwise every save or delete is refused at
runtime, and no unit test will catch it.** Two screens have already been caught
this way: editing `PlatformConfig` from Administration → Global needs
`platformconfig-admin-role`, and deleting another person's home volume needs
`workspace-volume-admin-role`.

`workspace-volume-admin-role` deliberately grants no `create`. Claims are made
by the workspace StatefulSet's `volumeClaimTemplate`; an administrator needs to
remove a departed user's storage, never to make any.

Per-user RBAC created by the `UserSpace` reconciler:

- Role `dwpk-workspace-user` in the user's namespace, full CRUD on `Workspace` plus pod/log/event reads
- RoleBinding `dwpk-owner`, binding the human owner's OIDC `User` and the `session` ServiceAccount to that Role
- Role `dwpk-workspace-reader`, the same shape as `dwpk-workspace-user` minus every write verb
- RoleBinding `dwpk-reader`, binding the `session-readonly` ServiceAccount to that Role
- RoleBinding `dwpk-workspace-edit`, binding the `workspace` ServiceAccount (the pod's own identity) to the built-in `edit` ClusterRole inside that namespace
- ClusterRole `dwpk-userspace-<userspace-name>`
- ClusterRoleBinding `dwpk-userspace-<userspace-name>`, binding the owner's `User`, `session`, and `session-readonly`

That ClusterRole is narrow by design. It grants `get`, `watch`, and `patch` on one `UserSpace` via `resourceNames` (`patch` only lets a person save their own SSH keys - the validator refuses any other field), plus catalog reads and the `use` verb on `WorkspaceImage`.

## Roles

`UserSpace.spec.role` takes two values:

| Role | Reach |
| --- | --- |
| `user` (default) | Their own workspaces, in their own namespace |
| `admin` | The whole platform |

An `admin` is granted the platform admin ClusterRoles by the `UserSpace`
reconciler, as ClusterRoleBindings against their session ServiceAccount.
Demoting them removes those bindings - garbage collection alone would not, since
it only fires when the `UserSpace` itself is deleted.

**Only an admin may grant the `admin` role.** A validating webhook checks the
requester with a `SubjectAccessReview` before allowing it. Without that check
the role field would be a privilege escalation: anyone able to edit a
`UserSpace`, including their own, could promote themselves.

```sh
kubectl patch userspace alice --type=merge -p '{"spec":{"role":"admin"}}'
```

There is no middle tier. A group-of-users layer existed and was removed: it
decided catalog visibility and gave a manager reach into other people's
namespaces, and both turned out to be RBAC the cluster already expresses more
directly.

### Disabling a user

`spec.disabled` blocks login without deleting anything:

```sh
kubectl patch userspace alice --type=merge -p '{"spec":{"disabled":true}}'
```

Their namespace, workspaces and volumes are untouched, and both login paths -
OAuth2 and local - are refused while the flag is set. Use it for someone on
extended leave or an account under investigation, where deleting the `UserSpace`
would take the data with it.

## Quota and persistence

### Namespace quota

`UserSpace.spec.quota` maps to `ResourceQuota.hard`:

- `requests.cpu`
- `requests.memory`
- `requests.storage`
- `requests.<gpu resource>`, only when `quota.gpu` is above zero. A hard zero
  would refuse every pod that so much as mentions a GPU, which is correct but
  writes an object noisier than the setting behind it. The resource name comes
  from `PlatformConfig.spec.gpuResourceName`, `nvidia.com/gpu` by default

`quota.workspaces` is not in that list. A `ResourceQuota` counts objects that
exist, and a stopped workspace still exists while consuming no CPU or memory -
counting it would make stopping one pointless. The limit is enforced instead by
the validating webhook, which counts only the workspaces that are running.

The controller also creates a `LimitRange` with default requests of `100m` CPU and `128Mi` memory for any container created in the namespace without explicit requests.

### Workspace storage

A workspace uses one PVC from the `StatefulSet` volume claim template. The volume claim template name is `home`, so the PVC ends up named `home-<workspace-name>-0`.

The controller does not set an owner reference from the `Workspace` to that PVC,
and sets no `persistentVolumeClaimRetentionPolicy` on the `StatefulSet`. That is
deliberate: stopping a workspace, and deleting one, both leave the home
directory intact.

Removing it is therefore a separate, explicit act. The delete dialog in
`/admin/workspaces` offers it, ticked by default, and requires the workspace
name to be typed before it will do anything - the confirmation is checked on the
server, not only in the browser, so a scripted POST cannot skip it. `DELETE`
over the REST API never touches the volume.

The owner of a namespace may delete their own claims and nobody else's, and
cannot create one: the `StatefulSet` provisions storage, so being able to remove
your own volume is not being able to conjure new storage against the quota.

An orphaned claim is easy to find, and worth looking for after any period of
deleting workspaces by hand:

```sh
kubectl get pvc -A -l dwpk.devops-ia.io/workspace
```

### ConfigMaps and Secrets

dwpk itself does not create per-workspace ConfigMaps or Secrets. User-created ConfigMaps and Secrets live in the user's namespace because the `workspace` ServiceAccount is bound to the built-in `edit` ClusterRole there. Backup and retention for those objects is a cluster policy concern, not something dwpk automates.

## Observability toggles

`Workspace.spec.observability` has two boolean fields:

- `logsEnabled`
- `metricsEnabled`

Current implementation in `internal/workspace/statefulset.go`:

- `logsEnabled` writes pod annotation `dwpk.devops-ia.io/logs-enabled=<true|false>`
- `metricsEnabled` writes pod label `dwpk.devops-ia.io/metrics-enabled=<true|false>`

What the repo does not ship:

- A log shipping sidecar injector for that annotation
- A workspace `PodMonitor` or Prometheus scrape job keyed off that label

So these fields are hooks for cluster-level observability automation. Turning them on in the CRD only has an effect if your cluster has something watching that annotation or label.

## Admin UI screens

The UI has six admin routes:

- `/admin/overview` - the platform at a glance: people, workspaces, catalog
  entries, and what is running now
- `/admin/users` - one row per person: their `UserSpace` joined to any local
  password logins sharing its owner. Role, quota and account status are changed
  here, and new users are added from the dialog at the top
- `/admin/workspaces` - every workspace in the cluster, with start, stop and
  delete. The delete dialog is where the home volume can be removed with the
  workspace
- `/admin/quota` - per-namespace usage against limit, computed from the
  `Workspace` objects that are actually running
- `/admin/catalog` - create, edit, deprecate and delete `WorkspaceImage` entries
- `/admin/settings` - the platform name and logo, the GPU resource name, and a
  read-only table of every environment variable the UI process read at startup

`/admin/users` replaced two earlier screens, `/admin/userspaces` and a separate
local-user list. Keeping them apart meant nothing told you that a login had no
`UserSpace` behind it, which is an account that exists and cannot sign in. The
merged screen shows that row and says so.

Each screen acts under the session's own token, so the API server decides what
it may do. `/admin/users` also runs a `SelfSubjectAccessReview` before
rendering, which saves showing a form that would 403 on submit.

Every signed-in user has `/profile`: their role, quota with usage, and their own
workspaces. Someone who signs in with a local password can change it
there; an OAuth2 user is told their password lives with their provider.

## Session and token lifecycle

UI sessions are server-side, in memory.

Defaults from code:

- UI session TTL: `15m`
- Login challenge TTL: `5m`
- Minted Kubernetes service account token lifetime: `1h`

Cookie names from `internal/ui/helpers.go`:

- `dwpk_ui_session`
- `dwpk_ui_login_state`
- `dwpk_ui_login_next`

Cookie behavior from `internal/ui/login.go` and `internal/ui/middleware.go`:

- The session cookie is `HttpOnly`.
- `Secure` follows `DWPK__UI_COOKIE_SECURE`, default `true`.
- `SameSite=Lax`.
- The cookie stores only an opaque session ID.
- The bearer token is re-minted through `TokenRequest` and kept server-side.
- Logout deletes the UI session only. It does not stop the workspace.

## Day-2 operations

### Review readiness and failures

```sh
kubectl get userspaces
kubectl get workspaces -A
kubectl describe workspace -n <namespace> <name>
kubectl logs -n dwpk-system deployment/dwpk-controller-manager
```

Look at:

- `status.phase`
- `status.conditions`
- webhook rejections on create or update
- image pull failures or scheduling failures on the workspace pod

### Rotate OAuth client secrets

Update the Secret referenced by `ui.oauth.existingSecret`, then restart the UI Deployment so new environment values are loaded.

### Rotate the gateway host key

If the gateway uses `gateway.hostKey.existingSecret`, replace that Secret and restart the gateway pods. If the chart uses the default `emptyDir` host key storage, every pod restart rotates the host key already.

## Backup and recovery

What to back up:

- CRDs and CR instances: `WorkspaceImage`, `UserSpace`, `Workspace`
- User namespaces created by the `UserSpace` reconciler
- PVCs for workspace home directories
- Any user-created ConfigMaps and Secrets in those namespaces
- OAuth client Secret and optional gateway host key Secret in `dwpk-system`

What recovery looks like:

1. Restore the control plane components.
2. Restore CRDs.
3. Restore `WorkspaceImage` and `UserSpace` objects.
4. Restore user namespaces and PVCs.
5. Restore `Workspace` objects.

Because home PVCs outlive the `Workspace`, storage recovery depends on preserving the namespace and the PVC. dwpk does not have its own backup controller.
