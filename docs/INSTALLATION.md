# Installation

## What ships today

Two install paths exist:

- `dist/install.yaml`, a raw `kubectl apply` bundle you build yourself with `make build-installer`
- the Helm chart, published as an OCI artifact at `oci://ghcr.io/devops-ia/helm-dwpk/dwpk` from the
  separate [devops-ia/helm-dwpk](https://github.com/devops-ia/helm-dwpk) repository

They are not equivalent.

`dist/install.yaml` comes from `config/default` and installs the manager, CRDs, webhook objects, and supporting RBAC. It does not include the SSH gateway or the UI.

The Helm chart templates all three components: manager, gateway, and UI. Use it for the full platform.

## Prerequisites

### Cluster requirements

The chart does not declare a `kubeVersion` gate. Current code uses:

- `k8s.io/api v0.36.0`
- `k8s.io/client-go v0.36.0`
- `sigs.k8s.io/controller-runtime v0.24.1`

The manifests rely on:

- CRDs in `apiextensions.k8s.io/v1`
- admission webhooks in `admissionregistration.k8s.io/v1`
- the `serviceaccounts/token` subresource for `TokenRequest`

Use a current Kubernetes release that supports those APIs.

### Required cluster add-ons

- cert-manager. The project relies on cert-manager for webhook certificates. The chart's default `certManager.enabled=true` path mounts the `webhook-server-cert` Secret into the manager pod and injects the CA bundle into the webhook configurations.
- An ingress controller if you want the UI exposed through `ui.ingress.enabled=true`, or a Gateway API implementation (with its CRDs installed) if you want `ui.gateway.enabled=true` instead.
- A load balancer implementation if you want the gateway `Service` of type `LoadBalancer` to get an external address.

[Quick start](./QUICKSTART.md) installs cert-manager like this:

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.21.1/cert-manager.yaml
kubectl wait --for=condition=Available -n cert-manager deployment --all --timeout=120s
```

### Image requirements

Optional. `.github/workflows/github-release.yml` already builds and publishes all three images
(`ghcr.io/devops-ia/dwpk`, `dwpk-gateway`, `dwpk-ui`) on every tagged release, and the Helm chart
defaults to them. Build your own only if you need a custom build.

The repo ships three Dockerfiles: `Dockerfile` (repo root, manager), `cmd/gateway/Dockerfile`, and `cmd/ui/Dockerfile`. Build all three with:

```sh
make docker-build-all IMG=<registry>/manager:<tag> GATEWAY_IMG=<registry>/gateway:<tag> UI_IMG=<registry>/ui:<tag>
```

or individually via `make docker-build`, `make docker-build-gateway`, `make docker-build-ui` (each has a matching `docker-push-*` target). Pass the resulting repositories/tags through `image.repository`/`image.tag`, `gateway.image.repository`/`gateway.image.tag`, and `ui.image.repository`/`ui.image.tag` in Helm values.

- `make helm-deploy` only wires the manager image through `IMG`. It does not set gateway or UI image values - set `--set gateway.image.repository=... --set ui.image.repository=...` (or a values file) alongside it.

## OAuth2 provider configuration

The UI reads provider settings from environment variables. The Helm chart maps those from `ui.oauth.*` values.

Supported providers:

| Provider | Required values | Optional value when `ui.baseURL` is set | Callback path |
| --- | --- | --- | --- |
| Entra ID | `ui.oauth.entraID.clientID`, `ui.oauth.entraID.issuerURL`, secret key for client secret | `ui.oauth.entraID.redirectURL` | `/callback/entra-id` |
| Google | `ui.oauth.google.clientID`, `ui.oauth.google.issuerURL`, secret key for client secret | `ui.oauth.google.redirectURL` | `/callback/google` |
| GitLab | `ui.oauth.gitLab.clientID`, `ui.oauth.gitLab.issuerURL`, secret key for client secret | `ui.oauth.gitLab.redirectURL` | `/callback/gitlab` |
| Keycloak | `ui.oauth.keycloak.clientID`, `ui.oauth.keycloak.issuerURL`, secret key for client secret | `ui.oauth.keycloak.redirectURL` | `/callback/keycloak` |
| GitHub | `ui.oauth.gitHub.clientID`, secret key for client secret | `ui.oauth.gitHub.redirectURL` | `/callback/github` |

Every variable is prefixed `DWPK__`, a double underscore, followed by the component and the setting.

Real environment variable names from `cmd/ui/main.go`:

- `DWPK__UI_LISTEN_ADDRESS`
- `DWPK__UI_PORT` - port only, on all interfaces; `DWPK__UI_LISTEN_ADDRESS` wins when both are set
- `DWPK__UI_BASE_PATH` - path prefix when the UI is served behind a proxy under a non-root path
- `DWPK__UI_LOG_LEVEL` - `debug`, `info`, `warn` or `error`
- `DWPK__UI_KUBECONFIG`
- `DWPK__UI_GATEWAY_HOST`
- `DWPK__UI_BASE_URL`
- `DWPK__UI_SESSION_TTL`
- `DWPK__UI_COOKIE_SECURE`
- `DWPK__UI_TOKEN_NAMESPACE` - namespace holding the API token Secrets
- `DWPK__UI_LOCAL_AUTH_ENABLED` - username/password login, off by default
- `DWPK__UI_LOCAL_AUTH_NAMESPACE` - namespace holding the local user Secrets
- `DWPK__UI_PROVIDER_ENTRA_ID_CLIENT_ID`
- `DWPK__UI_PROVIDER_ENTRA_ID_CLIENT_SECRET`
- `DWPK__UI_PROVIDER_ENTRA_ID_ISSUER_URL`
- `DWPK__UI_PROVIDER_ENTRA_ID_REDIRECT_URL`
- `DWPK__UI_PROVIDER_GOOGLE_CLIENT_ID`
- `DWPK__UI_PROVIDER_GOOGLE_CLIENT_SECRET`
- `DWPK__UI_PROVIDER_GOOGLE_ISSUER_URL`
- `DWPK__UI_PROVIDER_GOOGLE_REDIRECT_URL`
- `DWPK__UI_PROVIDER_GITLAB_CLIENT_ID`
- `DWPK__UI_PROVIDER_GITLAB_CLIENT_SECRET`
- `DWPK__UI_PROVIDER_GITLAB_ISSUER_URL`
- `DWPK__UI_PROVIDER_GITLAB_REDIRECT_URL`
- `DWPK__UI_PROVIDER_KEYCLOAK_CLIENT_ID`
- `DWPK__UI_PROVIDER_KEYCLOAK_CLIENT_SECRET`
- `DWPK__UI_PROVIDER_KEYCLOAK_ISSUER_URL`
- `DWPK__UI_PROVIDER_KEYCLOAK_REDIRECT_URL`
- `DWPK__UI_PROVIDER_GITHUB_CLIENT_ID`
- `DWPK__UI_PROVIDER_GITHUB_CLIENT_SECRET`
- `DWPK__UI_PROVIDER_GITHUB_REDIRECT_URL`

If `DWPK__UI_BASE_URL` is set and a provider-specific redirect URL is omitted, the UI derives the callback as `<baseURL>/callback/<provider>`.

Create the Secret referenced by `ui.oauth.existingSecret` with the default key names from `helm-dwpk/values.yaml`:

```sh
kubectl create secret generic dwpk-ui-oauth \
  -n dwpk-system \
  --from-literal=entra-id-client-secret='<entra-client-secret>' \
  --from-literal=google-client-secret='<google-client-secret>' \
  --from-literal=gitlab-client-secret='<gitlab-client-secret>' \
  --from-literal=keycloak-client-secret='<keycloak-client-secret>' \
  --from-literal=github-client-secret='<github-client-secret>'
```

Only create keys for providers you actually enable.

### Provider setup examples

Each provider needs an app registration pointing at `<baseURL>/callback/<provider>` and, for the
four OIDC providers, an issuer URL for discovery. `<baseURL>` is `ui.baseURL`
(`DWPK__UI_BASE_URL`).

#### Entra ID

1. Azure Portal → **Microsoft Entra ID → App registrations → New registration**. Add a **Web**
   redirect URI: `https://dwpk.example.com/callback/entra-id`.
2. Under **Certificates & secrets**, create a client secret.
3. The issuer URL is `https://login.microsoftonline.com/<tenant-id>/v2.0`.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system --create-namespace \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.entraID.clientID=<entra-client-id> \
  --set ui.oauth.entraID.issuerURL=https://login.microsoftonline.com/<tenant-id>/v2.0
```

See [Mapping OAuth2 groups](#mapping-oauth2-groups-to-adminuser-roles) below for a worked example
of Entra ID's admin/user group claim.

#### Google

1. [Google Cloud Console](https://console.cloud.google.com/) → **APIs & Services → Credentials →
   Create Credentials → OAuth client ID**, application type **Web application**.
2. Add an authorized redirect URI: `https://dwpk.example.com/callback/google`.
3. The issuer URL is `https://accounts.google.com`.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system --create-namespace \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.google.clientID=<google-client-id> \
  --set ui.oauth.google.issuerURL=https://accounts.google.com
```

Google's ID token has no `groups` claim, so `_ADMIN_GROUPS`/`_USER_GROUPS` have no effect here -
manage `UserSpace.spec.role` directly for Google logins.

#### GitLab

1. GitLab → **User Settings → Applications** (or a group/instance-level application if you want
   every member of that group to be able to log in). Redirect URI:
   `https://dwpk.example.com/callback/gitlab`. Scopes: `openid`, `email`, `profile`.
2. The issuer URL is `https://gitlab.com` for GitLab.com, or your self-managed instance's own URL.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system --create-namespace \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.gitLab.clientID=<gitlab-client-id> \
  --set ui.oauth.gitLab.issuerURL=https://gitlab.com
```

GitLab can carry a `groups` claim (full paths, e.g. `my-group/my-subgroup`) - set
`DWPK__UI_PROVIDER_GITLAB_ADMIN_GROUPS`/`_USER_GROUPS` to those paths if you want group-based roles.

#### Keycloak

1. Keycloak admin console → your realm → **Clients → Create client**, client type **OpenID
   Connect**, **Client authentication** on (confidential client). Redirect URI:
   `https://dwpk.example.com/callback/keycloak`.
2. The issuer URL is `https://<keycloak-host>/realms/<realm>`.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system --create-namespace \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.keycloak.clientID=<keycloak-client-id> \
  --set ui.oauth.keycloak.issuerURL=https://keycloak.example.com/realms/dwpk
```

Keycloak needs a client scope mapper that adds a `groups` claim to the ID token before
`_ADMIN_GROUPS`/`_USER_GROUPS` will see anything - realm roles alone are not on the token by
default.

#### GitHub

1. GitHub → **Settings → Developer settings → OAuth Apps → New OAuth App**. Authorization callback
   URL: `https://dwpk.example.com/callback/github`.
2. GitHub has no OIDC discovery document, so there is no issuer URL to set - the UI talks to
   GitHub's OAuth2 and REST endpoints directly.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system --create-namespace \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.gitHub.clientID=<github-client-id>
```

GitHub has no group claim at all - `_ADMIN_GROUPS`/`_USER_GROUPS` have no effect for
`DWPK__UI_PROVIDER_GITHUB_*`. Manage `UserSpace.spec.role` directly for GitHub logins.

You can enable more than one provider at once; each reads its own set of
`DWPK__UI_PROVIDER_<PROVIDER>_*` variables independently, and every configured provider appears as
a separate button on the login page.

### Mapping OAuth2 groups to admin/user roles

Every UserSpace already carries its own `spec.role`, set once by an admin
when the UserSpace is provisioned. For OIDC providers whose ID token carries
a `groups` claim (Entra ID, Keycloak and GitLab all can - GitHub has no
equivalent, since it maps org/team membership through a separate API
instead), you can additionally let group membership decide the role at
login time, the same way you might in any other app that reads
group-based authorization from its identity provider.

Two more environment variables per provider, comma-separated group
names/object IDs:

- `DWPK__UI_PROVIDER_<PROVIDER>_ADMIN_GROUPS`
- `DWPK__UI_PROVIDER_<PROVIDER>_USER_GROUPS`

Neither variable set (the default) means group mapping is off entirely -
`spec.role` alone decides, exactly as before this existed. When either is
set, every login through that provider is checked in this order:

1. A group in `ADMIN_GROUPS` → session role is `admin`, no matter what
   `spec.role` says.
2. Else a group in `USER_GROUPS` → session role is `user`.
3. Else neither list matches → `spec.role` (or its default, `user`) is
   used unchanged.

This mapping only ever adds admin access through a group; it never takes
admin access away from a UserSpace an administrator already flagged
`spec.role: admin` directly. To force a role regardless of group
membership, leave both variables unset for that provider and manage
`spec.role` by hand instead.

#### Worked example: Microsoft Entra ID

1. In the Entra ID app registration used for `DWPK__UI_PROVIDER_ENTRA_ID_*`,
   open **Token configuration** and add a **groups claim**
   (`GroupMembershipClaims`) to the ID token - either "Security groups" or,
   for larger tenants, "Groups assigned to the application" so the claim
   stays under Entra ID's per-token group limit.
2. In **Enterprise applications → (your app) → Users and groups**, note the
   **Object ID** of the group you want to grant admin access, and the
   Object ID of the group you want to grant ordinary user access (these can
   be the same group's parent/child structure, or two unrelated groups).
3. Set the two variables using those Object IDs:

   ```sh
   DWPK__UI_PROVIDER_ENTRA_ID_ADMIN_GROUPS=11111111-2222-3333-4444-555555555555
   DWPK__UI_PROVIDER_ENTRA_ID_USER_GROUPS=66666666-7777-8888-9999-000000000000
   ```

4. A user in the first group signs in and lands with `admin`; a user only
   in the second lands with `user`; a user in neither keeps whatever
   `spec.role` their UserSpace already has.

`DWPK__UI_BASE_URL` (already required for the redirect URL above) is the
same "the app needs to know its own external URL for callbacks" setting
most OIDC-based platforms ask for - nothing extra to configure for that
part.

## Install with raw manifests

Use this path if you only want the operator stack or you want a zero-Helm install.

1. Build or refresh the bundle.

```sh
make build-installer IMG=<registry>/dwpk-manager:<tag>
```

1. Apply it.

```sh
kubectl apply -f dist/install.yaml
```

1. Verify CRDs and manager pods.

```sh
kubectl get crd | grep 'dwpk.devops-ia.io'
kubectl get pods -n dwpk-system
kubectl get mutatingwebhookconfigurations,validatingwebhookconfigurations | grep dwpk
```

What this path installs:

- CRDs for `workspaceimages`, `userspaces`, and `workspaces`
- Manager Deployment
- Webhook Service and webhook configurations
- cert-manager `Issuer` and `Certificate` references from `config/default`
- Manager RBAC, helper CRD roles, leader election RBAC, and metrics RBAC

What it does not install:

- SSH gateway Deployment or Service
- UI Deployment, Service, or Ingress

## Install full platform with Helm

Use the chart published at `oci://ghcr.io/devops-ia/helm-dwpk/dwpk` for the full stack. Pick a chart
version from the [helm-dwpk releases](https://github.com/devops-ia/helm-dwpk/releases) - the chart
version tracks its own release cadence, separate from `dwpk`'s own tags.

### Values that matter first

Important defaults (see `helm show values oci://ghcr.io/devops-ia/helm-dwpk/dwpk --version
<chart-version>` for the full, current list):

- `manager.enabled=true`
- `gateway.enabled=true`
- `ui.enabled=true`
- `gateway.listenAddress=":2222"`
- `gateway.service.type=LoadBalancer`
- `gateway.service.port=22`
- `ui.listenAddress=":8080"`
- `ui.service.type=ClusterIP`
- `ui.service.port=80`
- `ui.sessionTTL=15m`
- `ui.cookieSecure=true`
- `metrics.enabled=true`
- `metrics.port=8443`
- `metrics.secure=true`
- `webhook.enabled=true`
- `webhook.port=9443`
- `certManager.enabled=true`
- `crd.enabled=true`
- `crd.keep=true`
- `rbac.helpers.enabled=false`

The chart supports any release namespace, but the manager binary hard-codes `dwpk-system` as its leader election namespace. Install into `dwpk-system` unless you patch the binary or code.

### Example Helm install

This example keeps all three components enabled and wires one Entra ID client. Component images
default to the `ghcr.io/devops-ia/dwpk{,-gateway,-ui}` images built by
`.github/workflows/github-release.yml`, tagged to match the chart's `appVersion` - only override
`*.image.repository`/`*.image.tag` if you publish your own build.

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  --namespace dwpk-system \
  --create-namespace \
  --set manager.replicas=2 \
  --set ui.baseURL=https://dwpk.example.com \
  --set ui.gatewayHost=dwpk.example.com \
  --set ui.ingress.enabled=true \
  --set ui.ingress.className=<ingress-class> \
  --set ui.ingress.hosts[0].host=dwpk.example.com \
  --set ui.ingress.hosts[0].paths[0].path=/ \
  --set ui.ingress.hosts[0].paths[0].pathType=Prefix \
  --set ui.oauth.existingSecret=dwpk-ui-oauth \
  --set ui.oauth.entraID.clientID=<client-id> \
  --set ui.oauth.entraID.issuerURL=https://login.microsoftonline.com/<tenant>/v2.0
```

If you do not want the gateway or UI, set `gateway.enabled=false` and `ui.enabled=false`.

### SSH host key

The gateway presents one SSH host key to every client, generated on first install and stored in a
Kubernetes Secret the chart creates and keeps (`gateway.hostKey.existingSecret` overrides this with
your own). Every gateway replica and every restart mounts that same Secret, so `ssh-keyscan` or a
client's `known_hosts` entry stays valid across restarts and upgrades - an SSH client, and in
particular VS Code Remote-SSH, refuses to forward ports the moment it sees the host key change, which
is what an unpersisted key looks like from the outside.

### Exposing the UI: Ingress or Gateway API

Two mutually independent ways to expose the UI `Service` externally, enable one or the other (or neither, if you only need cluster-internal access):

- `ui.ingress.*` - a classic `networking.k8s.io/v1` `Ingress`.
- `ui.gateway.*` - the [Kubernetes Gateway API](https://kubernetes.io/docs/concepts/services-networking/gateway/) (`gateway.networking.k8s.io/v1` `Gateway` + `HTTPRoute`). Requires the Gateway API CRDs and a controller implementing them (nginx-gateway-fabric, Envoy Gateway, Cilium, Istio, etc.) already installed in the cluster.

With `ui.gateway.create=true` (default when `ui.gateway.enabled=true`), the chart creates its own `Gateway` object using `ui.gateway.listeners`, and the `HTTPRoute` attaches to it:

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  --namespace dwpk-system --create-namespace \
  --set ui.gateway.enabled=true \
  --set ui.gateway.className=nginx \
  --set ui.gateway.hostnames[0]=dwpk.example.com
```

To attach to a `Gateway` that already exists (managed outside this chart, shared across apps), set `ui.gateway.create=false` and point `ui.gateway.parentRefs` at it instead:

```yaml
ui:
  gateway:
    enabled: true
    create: false
    parentRefs:
      - name: shared-gateway
        namespace: gateway-system
    hostnames:
      - dwpk.example.com
```

`ui.gateway.rules` controls `HTTPRoute` matches/filters (path prefix by default); the `backendRefs` entry pointing at the UI Service is always added automatically.

### Gateway host key

The gateway chart values support two modes:

- Ephemeral host key per pod start, the default when `gateway.hostKey.existingSecret=""`
- A fixed PEM key from a Secret when `gateway.hostKey.existingSecret` is set

The default Secret key name is `hostkey.pem`, and the file is mounted to `gateway.hostKey.path`, which defaults to `/var/run/dwpk-gateway/hostkey/hostkey.pem`.

Use a fixed host key for stable SSH host identity across pod restarts.

### Helm-specific gotchas

- `rbac.namespaced=true` is not compatible with the gateway or UI templates. Both templates call `fail` and require cluster-scoped RBAC.
- `make helm-deploy` only wires the manager image through `IMG`. It does not set gateway or UI image values.
- `crd.keep=true` means `helm uninstall` leaves the CRDs behind.

## Verify the install

### Core checks

```sh
kubectl get pods -n dwpk-system
kubectl get svc -n dwpk-system
kubectl get crd | grep 'dwpk.devops-ia.io'
kubectl get mutatingwebhookconfigurations,validatingwebhookconfigurations | grep dwpk
```

### Gateway checks

```sh
kubectl get deployment,svc -n dwpk-system | grep gateway
kubectl get svc -n dwpk-system <gateway-service-name> -o wide
```

The gateway Deployment listens on container port `2222` by default and the Service exposes port `22`.

### UI checks

```sh
kubectl get deployment,svc,ingress -n dwpk-system | grep ui
kubectl describe ingress -n dwpk-system <ui-ingress-name>
```

The UI liveness and readiness probes hit `GET /login`.

Once the UI is exposed, `/api/docs` serves the interactive API reference
(Swagger UI, vendored - no CDN, works offline) with a "Try it out" button
that can exercise real requests. It needs no login, since it is read-only
documentation. The machine-readable spec behind it lives at
`/api/v1/openapi.yaml` and is checked into the repository at
`docs/openapi.yaml`.

### Functional checks

1. Create at least one `WorkspaceImage` and one `UserSpace`.
2. Open the UI `/login` page.
3. Complete login with a provider that maps to the `UserSpace.spec.owner` email.
4. Confirm the catalog loads.
5. Create a `Workspace` and wait for `status.phase=Running`.
6. Confirm `status.endpoint` contains `<workspace-name>@<gateway-host>`.

## Upgrade

### Raw manifests

Rebuild the installer bundle with the new manager image and re-apply it.

```sh
make build-installer IMG=<registry>/dwpk-manager:<tag>
kubectl apply -f dist/install.yaml
```

### Helm

Run the same `helm upgrade --install` command with a new `--version` (chart) or new `*.image.tag` values.

## CI coverage in this repo

Workflow files under `.github/workflows/` currently do this:

- `app-lint.yml` runs `make lint-config` and `make lint`
- `app-test.yml` runs `go mod tidy` and `make test`
- `app-test-e2e.yml` installs kind, then runs `go mod tidy` and `make test-e2e`
- `github-pr-title.yml` checks pull request titles against the Conventional Commits format `CONTRIBUTING.md` requires
- `github-release.yml` runs semantic-release on `main`, then builds and publishes the `manager`/`gateway`/`ui` binaries and container images (to both Docker Hub and `ghcr.io`) for the resolved version
- `github-sync-chart-crds.yml` opens a pull request against `devops-ia/helm-dwpk` whenever `api/v1alpha1` changes, keeping that repo's CRDs and manager `ClusterRole` in sync
- `github-pages.yml` publishes this repository's `docs/` set to GitHub Pages on every change

Chart linting, packaging, and the OCI push to `ghcr.io/devops-ia/helm-dwpk/dwpk` happen in the
separate `devops-ia/helm-dwpk` repository's own workflows, not here.

## Uninstall

### Helm

```sh
helm uninstall dwpk -n dwpk-system
```

Then delete CRDs if you want a full cleanup:

```sh
make uninstall
```

### Raw manifests

```sh
make undeploy
make uninstall
```

Before removing CRDs, delete user-created `Workspace`, `UserSpace`, and `WorkspaceImage` objects if you want Kubernetes garbage collection to clean up namespaces and workload objects first. Persistent home PVCs are deliberately retained by the `StatefulSet` design, so remove them manually if you want the storage gone.
