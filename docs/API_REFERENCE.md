# API reference

The REST API's machine-readable schema is at [`openapi.yaml`](./openapi.yaml).

## Group and version

- API group: `dwpk.devops-ia.io`
- Version: `v1alpha1`

## Kinds and scope

| Kind | Scope |
| --- | --- |
| `WorkspaceImage` | Cluster |
| `UserSpace` | Cluster |
| `Workspace` | Namespaced |
| `ImageRegistry` | Cluster |

## Common status shape

Every CRD listed above uses `status.conditions` as `[]metav1.Condition`.

Each condition item has the standard Kubernetes fields:

- `type`
- `status`, one of `True`, `False`, `Unknown`
- `reason`
- `message`
- `lastTransitionTime`
- `observedGeneration`, optional

## WorkspaceImage

Scope: cluster.

Printer columns from `api/v1alpha1/workspaceimage_types.go`:

- `Display Name` from `.spec.displayName`
- `Image` from `.spec.image`
- `Deprecated` from `.spec.deprecated`
- `Age` from `.metadata.creationTimestamp`

### spec

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `spec.displayName` | `string` | Yes | None | None | Catalog title shown to users. |
| `spec.description` | `string` | No | None | None | Catalog description text. |
| `spec.icon` | `string` | No | None | None | Icon URL. |
| `spec.tags[]` | `string` | No | None | None | Tag list used by the UI filters. |
| `spec.maintainer` | `string` | No | None | None | Contact string for the entry. |
| `spec.deprecated` | `boolean` | No | `false` | None | Hidden from the catalog unless the user asks to show deprecated entries. |
| `spec.image` | `string` | Yes | None | None | Container image reference for the workspace pod. |
| `spec.imagePullPolicy` | `corev1.PullPolicy` | No | `IfNotPresent` | `Always`, `IfNotPresent`, `Never` | Passed into the workspace container. |
| `spec.shell` | `string` | No | `/bin/bash` | None | Login shell the gateway execs. |
| `spec.homePath` | `string` | No | `/home/dev` | None | Working directory and PVC mount path. |
| `spec.runAsUser` | `integer` | No | `1000` | None | Also used as `fsGroup` on the pod. |
| `spec.command[]` | `string` | No | `['sleep','infinity']` | None | Container command. Keeps generic images alive by default. |
| `spec.allowRoot` | `boolean` | No | `false` | None | Lets a workspace from this entry run as uid 0. Administrator-only. |
| `spec.deprecateAt` | `metav1.Time` | No | None | None | The entry stays fully usable until this moment, with a warning on the catalog; after it, it behaves as `deprecated`. |
| `spec.placement` | `WorkspacePlacement` | No | None | None | Scheduling constraints. |
| `spec.placement.nodeSelector` | `map[string]string` | No | None | None | Copied into the pod spec. |
| `spec.placement.tolerations[]` | `corev1.Toleration` | No | None | Standard Kubernetes schema | Copied into the pod spec. |
| `spec.imagePullSecretRef` | `corev1.LocalObjectReference` | No | None | None | Names a pull Secret for a private image. dwpk replicates the Secret of this name from the manager's own namespace into every user namespace, so this field only ever needs to carry a name. An ImageRegistry sync stamps its own `imagePullSecretRef` here automatically. |

A catalog entry no longer carries sizes or storage defaults. Resources are chosen
per workspace and checked against the owner's quota at admission.

### status

| Field | Type | Required | Default | Validation | Notes |
|---|---|---|---|---|---|
| `status.conditions[]` | `metav1.Condition` | No | None | Standard Kubernetes schema | Current state of the catalog entry. |

### example

```yaml
apiVersion: dwpk.devops-ia.io/v1alpha1
kind: WorkspaceImage
metadata:
  name: python-3.12
spec:
  displayName: "Python 3.12"
  description: "Python 3.12 with poetry, ruff and the AWS CLI."
  icon: "https://cdn.example.com/icons/python.svg"
  tags:
    - python
    - backend
  maintainer: "platform@example.com"
  deprecated: false
  image: "ghcr.io/devops-ia/dwpk/python:3.12"
  imagePullPolicy: IfNotPresent
  shell: /bin/bash
  homePath: /home/dev
  runAsUser: 1000
  command:
    - sleep
    - infinity
  placement:
    nodeSelector:
      workload: worker
    tolerations: []
```

## ImageRegistry

Scope: cluster.

Configures one external container registry to sync into the catalog (§4.6). Several
ImageRegistry objects, including several of the same provider, may exist side by side -
each syncs and prunes only the WorkspaceImages it owns (`ownerReferences`, labelled
`dwpk.devops-ia.io/image-registry=<registry name>`).

Printer columns from `api/v1alpha1/imageregistry_types.go`:

- `Provider` from `.spec.provider`
- `Images` from `.status.images`
- `Last Sync` from `.status.lastSyncTime`
- `Age` from `.metadata.creationTimestamp`

### spec

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `spec.provider` | `string` | Yes | None | `aws-ecr` | Which registry API this object talks to. A second provider is a new enum value plus a new `registry.Provider` implementation, not a change to this type. |
| `spec.aws` | `AWSRegistry` | No | None | Required (CEL) when `spec.provider` is `aws-ecr` | AWS ECR configuration. |
| `spec.aws.region` | `string` | Yes | None | Max length 32 | AWS region the registry lives in. |
| `spec.aws.registryId` | `string` | No | None | Max length 12 | AWS account ID. Empty resolves to the account the credentials belong to - what IRSA, EKS Pod Identity and an instance profile all resolve to without this ever being set. |
| `spec.aws.roleArn` | `string` | No | None | Max length 2048 | Assumed via STS before listing, for a registry in another account. |
| `spec.sync.intervalSeconds` | `integer` | No | `900` | Minimum 60 | How often the registry is re-listed. |
| `spec.sync.include[]` | `string` | No | None | RE2 | Repository name patterns; empty means every repository is a candidate. |
| `spec.sync.exclude[]` | `string` | No | None | RE2 | Wins over include when both match. |
| `spec.sync.tags.mode` | `string` | No | `latest` | `latest`, `pattern` | Newest tag by push time, or every tag matching `patterns`. |
| `spec.sync.tags.patterns[]` | `string` | No | None | RE2 | Used only when `mode` is `pattern`. |
| `spec.sync.tags.limit` | `integer` | No | `1` | Minimum 1 | Newest N matching tags per repository become catalog entries. |
| `spec.sync.prune` | `boolean` | No | `false` | None | Deletes a WorkspaceImage this registry previously created once its remote image is gone. Off by default: a briefly unreachable registry should not delete a catalog entry somebody may be using. |
| `spec.imagePullSecretRef` | `corev1.LocalObjectReference` | No | None | None | Names a Secret, in the manager's own namespace, that dwpk replicates into every user namespace. Stamped onto every WorkspaceImage this registry creates. |

Force-syncing outside the configured interval is not a field: bump the
`dwpk.devops-ia.io/force-sync` annotation to any new value and the standard watch
retriggers a sync immediately.

### status

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `status.conditions[]` | `metav1.Condition` | No | None | Standard Kubernetes schema | `Ready`/`Degraded`. A failed sync leaves `status.images` and `status.lastSyncTime` at their last known good value rather than clearing them. |
| `status.observedGeneration` | `integer` | No | None | None | The `metadata.generation` this status was computed from. |
| `status.lastSyncTime` | `metav1.Time` | No | None | None | When the registry was last successfully listed. |
| `status.images` | `integer` | No | None | None | How many WorkspaceImages this registry currently owns. |

### example

```yaml
apiVersion: dwpk.devops-ia.io/v1alpha1
kind: ImageRegistry
metadata:
  name: team-ecr
spec:
  provider: aws-ecr
  aws:
    region: eu-west-1
  sync:
    intervalSeconds: 900
    include: ["dwpk/.*"]
    exclude: ["dwpk/scratch.*"]
    tags:
      mode: latest
      limit: 1
    prune: false
```

## UserSpace

Scope: cluster.

Printer columns from `api/v1alpha1/userspace_types.go`:

- `Owner` from `.spec.owner`
- `Namespace` from `.status.namespace`
- `Phase` from `.status.phase`
- `Workspaces` from `.spec.quota.workspaces`
- `Age` from `.metadata.creationTimestamp`

### spec

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `spec.owner` | `string` | Yes | None | Minimum length 1; immutable by CEL rule `self == oldSelf` | Email or Kubernetes username that owns this space. |
| `spec.quota` | `UserSpaceQuota` | Yes | None | None | Namespace budget. |
| `spec.quota.cpu` | `resource.Quantity` | Yes | None | Kubernetes quantity syntax | Total requested CPU budget. |
| `spec.quota.memory` | `resource.Quantity` | Yes | None | Kubernetes quantity syntax | Total requested memory budget. |
| `spec.quota.storage` | `resource.Quantity` | Yes | None | Kubernetes quantity syntax | Total PVC storage budget. |
| `spec.quota.gpu` | `integer` | No | `0` | Minimum `0` | GPUs the running workspaces may hold at once, counted against `PlatformConfig.spec.gpuResourceName`. |
| `spec.quota.workspaces` | `integer` | Yes | None | Minimum `0` | Maximum number of `Workspace` objects in the namespace. |
| `spec.networkPolicy` | `string` | Yes | None | `Isolated`, `ClusterEgress` | Selects the egress posture the controller writes. |

### status

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `status.namespace` | `string` | No | None | None | Namespace reconciled for this user. |
| `status.phase` | `string` | No | None | None | Coarse summary of current state. Code exports `Pending`, `Ready`, `Failed`. |
| `status.observedGeneration` | `integer` | No | None | None | Generation the status reflects. |
| `status.conditions[]` | `metav1.Condition` | No | None | Standard Kubernetes schema | Readiness and degradation details. |

### example

```yaml
apiVersion: dwpk.devops-ia.io/v1alpha1
kind: UserSpace
metadata:
  name: alice
spec:
  owner: "alice@example.com"
  quota:
    cpu: "8"
    memory: 32Gi
    storage: 100Gi
    gpu: 0
    workspaces: 1
  networkPolicy: Isolated
```

There is no configurable floor under `spec.quota` - the minimum is always
zero. Only the maximum values above are ever set.

## Workspace

Scope: namespaced.

Printer columns from `api/v1alpha1/workspace_types.go`:

- `Image` from `.spec.imageRef.name`
- `Status` from `.status.phase`
- `Endpoint` from `.status.endpoint`
- `Age` from `.metadata.creationTimestamp`

### spec

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `spec.imageRef` | `WorkspaceImageReference` | Yes | None | None | Names the `WorkspaceImage` catalog entry. |
| `spec.imageRef.name` | `string` | Yes | None | Minimum length 1 | Cluster-scoped `WorkspaceImage` name. |
| `spec.resources` | `corev1.ResourceRequirements` | No | None | Every request must be at most its limit; an extended resource must have request equal to limit | CPU, memory and GPU for the container. Checked against the owner's remaining quota at admission. |
| `spec.nodeSelector` | `map[string]string` | No | The image's `placement.nodeSelector` | None | Overrides the catalog entry rather than merging with it. |
| `spec.tolerations[]` | `corev1.Toleration` | No | The image's `placement.tolerations` | Standard Kubernetes schema | Overrides the catalog entry. |
| `spec.affinity` | `corev1.Affinity` | No | None | Standard Kubernetes schema | Copied into the pod spec. |
| `spec.env[]` | `corev1.EnvVar` | No | None | Standard Kubernetes schema | Copied into the container. |
| `spec.envFrom[]` | `corev1.EnvFromSource` | No | None | Standard Kubernetes schema | Whole Secrets and ConfigMaps as environment. |
| `spec.volumes[]` | `corev1.Volume` | No | None | Standard Kubernetes schema | Extra volumes beside the home PVC. |
| `spec.volumeMounts[]` | `corev1.VolumeMount` | No | None | Standard Kubernetes schema | Where those volumes land in the container. |
| `spec.storage` | `resource.Quantity` | No | None | Immutable after first set by CEL rule | Size of the home PVC. Counted against `UserSpace.spec.quota.storage` at admission. |
| `spec.sshAuthorizedKeys[]` | `string` | No | None | Each key must start with `ssh-`, `ecdsa-sha2-` or `sk-` | OpenSSH public keys accepted by the gateway. |
| `spec.running` | `boolean` | No | `true` | If `true`, the object must have at least one SSH key | Stop and start switch for the `StatefulSet`. No `omitempty` in Go type. |
| `spec.idleTimeout` | `metav1.Duration` | No | `4h` | None | Present in the API, but auto-stop is not implemented yet. |
| `spec.observability` | `WorkspaceObservability` | No | None | None | Per-workspace label and annotation toggles. |
| `spec.observability.logsEnabled` | `boolean` | No | `true` | None | Writes pod annotation `dwpk.devops-ia.io/logs-enabled`. |
| `spec.observability.metricsEnabled` | `boolean` | No | `true` | None | Writes pod label `dwpk.devops-ia.io/metrics-enabled`. |

Object-level CEL validations on `Workspace.spec`:

- `storage` is immutable once set
- every `sshAuthorizedKeys` entry is an OpenSSH public key: `ssh-rsa`, `ssh-ed25519`,
  `ecdsa-sha2-nistp256/384/521`, or an `sk-` security key
- `running=true` requires at least one SSH key

The validating webhook adds what CEL cannot see, because it needs another object:
the name must be unique in the namespace, and the requested resources must fit in
what the owner's `UserSpace` quota has left.

### status

| Field | Type | Required | Default | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| `status.phase` | `string` | No | None | `Pending`, `Starting`, `Running`, `Suspended`, `Failed` | Current coarse lifecycle state. |
| `status.endpoint` | `string` | No | None | None | Published as `<workspace-name>@<gateway-host>`. |
| `status.podName` | `string` | No | None | None | Current pod name. Usually `<workspace-name>-0`. |
| `status.lastActivityTime` | `metav1.Time` | No | None | None | Updated by the gateway on session open and close. |
| `status.observedGeneration` | `integer` | No | None | None | Generation the status reflects. |
| `status.conditions[]` | `metav1.Condition` | No | None | Standard Kubernetes schema | Includes `Ready`, `Degraded`, and `ImageResolved` in current controller code. |

### admission behavior

On CREATE, the mutating webhook also stamps this annotation:

| Field | Type | Set by | Notes |
|---|---|---|---|
| `metadata.annotations["dwpk.devops-ia.io/requester"]` | `string` | Mutating webhook | Authenticated username from `request.userInfo.username` |

### example

```yaml
apiVersion: dwpk.devops-ia.io/v1alpha1
kind: Workspace
metadata:
  name: dev
  namespace: dwpk-alice
spec:
  imageRef:
    name: python-3.12
  resources:
    requests:
      cpu: "500m"
      memory: 1Gi
    limits:
      cpu: "2"
      memory: 4Gi
  storage: 20Gi
  sshAuthorizedKeys:
    - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForSamplesOnly000000000 alice@laptop"
  running: true
  idleTimeout: 4h
  observability:
    logsEnabled: true
    metricsEnabled: true
```

---

# REST API

The UI also serves a JSON API at `/api/v1`, so the same operations are available
to a script without driving a browser. It is a convenience over the Kubernetes
API, never a way around it.

## Authentication

Two ways in, both landing on the same server-side session:

- **Cookie** - `POST /api/v1/login` with `{"username","password"}`. The session
  id comes back as a `Set-Cookie`; the CSRF token for later writes comes back in
  a header, and again from `GET /api/v1/session`. Every mutating request must
  carry it as `X-CSRF-Token`.
- **Bearer** - `Authorization: Bearer dwpk_…`, using a token from
  `POST /api/v1/tokens`. No CSRF token is needed, since there is no cookie to
  ride on.

## Authorization

There is none here. Every call is made against the Kubernetes API with the
caller's own forwarded ServiceAccount token, so a person who cannot delete a
workspace receives the API server's own 403 - the same message, from the same
RBAC rule, as they would see in the browser or in `kubectl`. There is no second
implementation to keep in step. See SPEC §8.1.

## Conventions

- Lists come back as `{"items": [...]}`; single objects come back bare.
- Field names are `snake_case`. Kubernetes objects are returned as they are, so
  their own fields stay `camelCase`.
- `PATCH` bodies use optional fields: **an omitted key keeps its current value**
  rather than resetting it. Sending `{"role":"admin"}` to a disabled account
  leaves it disabled.
- Errors are `{"error": "<message>"}` with a matching status code.

## Endpoints

### Session

| Method | Path | Notes |
| --- | --- | --- |
| `POST` | `/api/v1/login` | Username and password; sets the session cookie |
| `GET` | `/api/v1/session` | Who you are: email, namespace, role, CSRF token |
| `POST` | `/api/v1/logout` | Clears the server-side session |

### Workspaces

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/workspaces` | **Scoped by role**: an administrator lists the cluster, everyone else their own namespace. `?namespace=` narrows the result |
| `POST` | `/api/v1/workspaces` | Creates in your own namespace |
| `GET` | `/api/v1/workspaces/{name}` | `?namespace=` reaches one you can see but do not own |
| `DELETE` | `/api/v1/workspaces/{name}?delete_volume=` | Deletes the workspace. **The home PVC (`home-{name}-0`) is deleted too by default** - a StatefulSet never removes its own claim, so the API does it explicitly, matching the web UI's delete dialog (also checked by default). Pass `?delete_volume=false` to keep the PVC. If the workspace deletes but the PVC delete fails, the response is `409 Conflict` naming exactly what's left |
| `POST` | `/api/v1/workspaces/{name}/start` | Patches `spec.running` |
| `POST` | `/api/v1/workspaces/{name}/stop` | Patches `spec.running` |

Resources are not patchable. `spec.resources` and `spec.storage` are set at
creation: storage because a PVC cannot shrink, and CPU and memory because
changing them replaces the pod, which the stop/start pair does explicitly.

### Catalog

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/workspace-images` | Only entries you may `use` |
| `GET` | `/api/v1/workspace-images/{name}` | |
| `POST` | `/api/v1/workspace-images` | Administrator only |
| `PATCH` | `/api/v1/workspace-images/{name}` | Administrator only. Omitted fields keep their current value |
| `DELETE` | `/api/v1/workspace-images/{name}` | Administrator only |
| `GET` | `/api/v1/image-registries` | Configured registries |
| `GET` | `/api/v1/image-registries/{name}` | |
| `POST` | `/api/v1/image-registries` | Administrator only |
| `PATCH` | `/api/v1/image-registries/{name}` | Administrator only. Omitted fields keep their current value |
| `DELETE` | `/api/v1/image-registries/{name}` | Administrator only. Every WorkspaceImage it synced is deleted too |
| `POST` | `/api/v1/image-registries/{name}/force-sync` | Administrator only. Retriggers a sync outside its configured interval |

Creating, editing and deleting catalog entries and registries over REST needs
the same administrator RBAC the web UI's catalog admin screen requires - this
is not a second, looser permission model.

### Administration

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/admin/userspaces` | Every UserSpace, with its quota and role |
| `POST` | `/api/v1/admin/userspaces` | Provisions a new UserSpace |
| `PATCH` | `/api/v1/admin/userspaces/{name}` | Role and disabled |
| `DELETE` | `/api/v1/admin/userspaces/{name}` | Deletes the UserSpace (not its Kubernetes namespace - the controller's own finalizer handles that) |
| `GET` | `/api/v1/admin/quota` | Usage against limit for every UserSpace |
| `PATCH` | `/api/v1/admin/quota/{name}` | Updates one UserSpace's quota. Omitted fields keep their current value |
| `GET` `POST` | `/api/v1/admin/local-users` | Only when local auth is enabled |
| `DELETE` | `/api/v1/admin/local-users/{name}` | |

### Profile and tokens

| Method | Path | Notes |
| --- | --- | --- |
| `POST` | `/api/v1/profile/password` | `{"current_password","new_password"}`. The account comes from your session, never from the body. Local logins only: a session from an identity provider gets `409` |
| `GET` `POST` | `/api/v1/tokens` | The plaintext token is returned once, by the issuing call only |
| `DELETE` | `/api/v1/tokens/{name}` | |

### Logs

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/v1/workspaces/{name}/logs` | Tail of the workspace container's logs. `?tail=` bounds line count, default matches the UI |

## Example

```sh
# Sign in and keep the cookie.
curl -sc jar -d '{"username":"alice","password":"..."}' \
  -H 'Content-Type: application/json' https://dwpk.example.com/api/v1/login

CSRF=$(curl -sb jar https://dwpk.example.com/api/v1/session | jq -r .csrf_token)

# Stop a workspace, then look at what is left.
curl -sb jar -X POST -H "X-CSRF-Token: $CSRF" \
  https://dwpk.example.com/api/v1/workspaces/dev/stop

curl -sb jar https://dwpk.example.com/api/v1/workspaces/dev | jq .status
```
