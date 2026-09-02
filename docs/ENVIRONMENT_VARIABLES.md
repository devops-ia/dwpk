# Environment variables

Every environment variable dwpk actually reads, grepped from the three
binaries' own `main.go` files so nothing here is invented. Flags
(`--metrics-bind-address`, `--leader-elect`, and so on - see `cmd/manager
--help`) are a separate mechanism and are not repeated here; the Helm chart's
`values.yaml` maps most of them for you. Where a value corresponds to a Helm
value, that mapping is noted.

## `dwpk-ui`

The web UI and REST API. All variables are read once at process start
(`cmd/ui/main.go`); nothing here is re-read while the process is running.

| Variable | Default | Notes |
| --- | --- | --- |
| `DWPK__UI_LISTEN_ADDRESS` | `:8080` (see `DWPK__UI_PORT`) | Full `host:port` bind address. Takes precedence over `DWPK__UI_PORT` when both are set. |
| `DWPK__UI_PORT` | `8080` | Port only, on all interfaces. Ignored if `DWPK__UI_LISTEN_ADDRESS` is set. |
| `DWPK__UI_KUBECONFIG` | in-cluster config | Path to a kubeconfig file. Leave unset when running inside the cluster - the Pod's own ServiceAccount is used instead. |
| `DWPK__UI_GATEWAY_HOST` | `dwpk.example.com` | Hostname shown in a workspace's SSH endpoint (`<workspace>@<gateway-host>`). Maps to `ui.gatewayHost` in Helm. |
| `DWPK__UI_BASE_URL` | none | The UI's own externally reachable URL, used to derive `<baseURL>/callback/<provider>` when a provider's own redirect URL is not set, and as the base for password-reset links. Maps to `ui.baseURL`. |
| `DWPK__UI_BASE_PATH` | none (root) | Path prefix when the UI is served behind a proxy under a non-root path, e.g. `/dwpk`. Maps to `ui.basePath`. |
| `DWPK__UI_SESSION_TTL` | `15m` | Idle session window, a Go duration (`30m`, `2h`). Maps to `ui.sessionTTL`. |
| `DWPK__UI_COOKIE_SECURE` | `true` | Whether the session cookie is marked `Secure`. Only turn this off for a plain-HTTP local/dev deployment; never in production. Maps to `ui.cookieSecure`. |
| `DWPK__UI_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. Maps to `ui.logLevel`. |
| `DWPK__UI_LOCAL_AUTH_ENABLED` | `false` | Turns on username/password login alongside OAuth2 (§7.8). Maps to `ui.localAuth.enabled`. |
| `DWPK__UI_LOCAL_AUTH_NAMESPACE` | `dwpk-system` | Namespace holding local user Secrets. Maps to `ui.localAuth.namespace`. |
| `DWPK__UI_TOKEN_NAMESPACE` | `dwpk-system` | Namespace holding API bearer token Secrets. Maps to `ui.tokenNamespace`. |

### Per-provider OAuth2 variables

Repeat the block below once per provider you enable, replacing `<PROVIDER>`
with `ENTRA_ID`, `GOOGLE`, `GITLAB`, `KEYCLOAK` or `GITHUB`. A provider with
none of its four required variables set is treated as not configured and is
not offered on the login page.

| Variable | Required | Notes |
| --- | --- | --- |
| `DWPK__UI_PROVIDER_<PROVIDER>_CLIENT_ID` | Yes | OAuth2/OIDC client ID. |
| `DWPK__UI_PROVIDER_<PROVIDER>_CLIENT_SECRET` | Yes | OAuth2 client secret. Put this in a Secret, not plaintext values - see `ui.oauth.existingSecret` in `docs/INSTALLATION.md`. |
| `DWPK__UI_PROVIDER_<PROVIDER>_ISSUER_URL` | Yes (all except GitHub) | OIDC discovery issuer. GitHub has no OIDC discovery document and does not use this variable. |
| `DWPK__UI_PROVIDER_<PROVIDER>_REDIRECT_URL` | No if `DWPK__UI_BASE_URL` is set | Explicit callback URL. Derived as `<baseURL>/callback/<provider>` when omitted. |
| `DWPK__UI_PROVIDER_<PROVIDER>_ADMIN_GROUPS` | No | Comma-separated group names/object IDs (§7.9). A login whose ID token `groups` claim matches one of these gets the `admin` role for that session, regardless of the UserSpace's own `spec.role`. Unset (with `_USER_GROUPS` also unset) means group mapping is off - `spec.role` alone decides, as before this existed. |
| `DWPK__UI_PROVIDER_<PROVIDER>_USER_GROUPS` | No | Comma-separated group names/object IDs. A login matching one of these (and none of `_ADMIN_GROUPS`) gets the `user` role for that session. |

GitHub has no group claim at all - it maps org/team membership through a
separate API instead - so `_ADMIN_GROUPS`/`_USER_GROUPS` have no effect for
`DWPK__UI_PROVIDER_GITHUB_*`. See `docs/INSTALLATION.md` for a worked Entra ID
example of the group-mapping variables.

Helm maps these from `ui.oauth.<provider>.*` values; see
`docs/INSTALLATION.md`'s OAuth2 provider configuration section for the exact
key names and the required Secret.

## `dwpk-manager` (the operator)

Controllers, webhooks and the one-shot admin bootstrap Job read these. Most
of the manager's configuration is flags, not environment variables - see
`cmd/manager --help` or `helm-dwpk/values.yaml`'s `manager.args`. Only the
admin bootstrap options are environment variables, since they run inside a
Helm post-install hook Job where flags are less convenient to template than
Secret-backed env vars.

| Variable | Default | Notes |
| --- | --- | --- |
| `DWPK__ADMIN_USERSPACE` | `admin` | Name of the UserSpace created for the bootstrap admin. |
| `DWPK__ADMIN_USERNAME` | `admin` | Local username for the bootstrap admin login. |
| `DWPK__ADMIN_EMAIL` | `admin@dwpk.local` | Owner email recorded on the bootstrap UserSpace. |
| `DWPK__ADMIN_PASSWORD` | generated | Password for the bootstrap admin. Left unset, a random password is generated and written once to a throwaway Secret for you to read and delete - see the Helm install output. Set explicitly only for a scripted/demo environment; never check a real value into source control. |

This bootstrap is idempotent: once the admin UserSpace and local user exist,
re-running the Job (a Helm upgrade, for example) does nothing further.

### Manager flags worth knowing

Two flags decide what a user sees as their SSH endpoint, and they interact:

| Flag | Helm value | Notes |
| --- | --- | --- |
| `--gateway-service` | derived | The gateway's own Service, as `<namespace>/<name>`. When that Service has a LoadBalancer address, the address is published in `Workspace.status.endpoint`. The chart sets this automatically whenever `gateway.enabled` is true; it is omitted otherwise. |
| `--gateway-host` | `manager.gatewayHost` | The fallback hostname, used when `--gateway-service` is unset, names a Service that does not exist, or names one with no LoadBalancer address yet. |

Resolution order is hostname, then IP, then the fallback. A hostname wins
because an internal AWS NLB publishes a hostname and no address, and the
hostname keeps working when the address behind it is reassigned.

This exists because a cluster-internal Service DNS name
(`dwpk-gateway.dwpk-system.svc.cluster.local`) is not resolvable from the VPN
users connect over, so it cannot be pasted into `ssh`.

## `dwpk-gateway`

The SSH gateway takes all of its configuration through command-line flags
(`--listen-address`, `--host-key-path`, and the standard `zap` logging flags)
rather than environment variables. See `cmd/gateway --help` or
`helm-dwpk/values.yaml`'s `gateway.listenAddress`, `gateway.hostKey.path` and
`gateway.extraArgs` for the full list.
