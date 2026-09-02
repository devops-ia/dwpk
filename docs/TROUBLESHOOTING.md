# Troubleshooting

## Controller is not reconciling

Check the manager first.

```sh
kubectl get pods -n dwpk-system
kubectl logs -n dwpk-system deployment/dwpk-controller-manager
kubectl get lease -n dwpk-system
```

Things to verify:

- At least one manager pod is ready.
- One pod holds the leader election Lease `dwpk-controller.dwpk.devops-ia.io`.
- The manager can talk to the webhook certificate Secret if webhooks are enabled.
- `Workspace` and `UserSpace` objects show current `status.conditions` and `status.observedGeneration`.

If you installed Helm into a namespace other than `dwpk-system`, leader election is a likely failure point. `cmd/manager/main.go` hard-codes `dwpk-system` as the leader election namespace.

## Webhook rejects a create or update

Common valid rejections from the current code:

- Referenced `WorkspaceImage` does not exist, or its entry is deprecated and this is a create
- Requested `spec.resources` (requests or limits) exceed `UserSpace.spec.quota`, workspace count included
- Namespace already has `UserSpace.spec.quota.workspaces` workspaces
- `Workspace.spec.storage` changed after create
- `Workspace.spec.sshAuthorizedKeys` contains an entry that does not start with `ssh-`
- `Workspace.spec.running=true` without any SSH key
- `UserSpace.spec.owner` changed after create

Debug steps:

```sh
kubectl describe workspace -n <namespace> <name>
kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration | grep dwpk
kubectl get pods -n dwpk-system
kubectl logs -n dwpk-system deployment/dwpk-controller-manager | grep -i webhook
```

If cert-manager is not ready, the webhook Service may exist before the serving certificate does.

## Gateway connection refused

Check the network path.

```sh
kubectl get deployment,svc -n dwpk-system | grep gateway
kubectl describe svc -n dwpk-system <gateway-service-name>
kubectl logs -n dwpk-system deployment/<gateway-deployment-name>
```

What to look for:

- Gateway Service has an address if you use `LoadBalancer`
- Service port `22` maps to target port `ssh`
- Gateway pod container listens on `:2222` by default
- No image pull or crash loop on gateway pods

If you used the chart defaults without publishing a real gateway image, the pod will fail before it can listen.

## Gateway auth fails

How gateway auth works in code:

- SSH username must equal `Workspace.metadata.name`
- Offered public key must exactly match one parsed key in `Workspace.spec.sshAuthorizedKeys`
- The matched workspace must be `status.phase=Running`

Debug steps:

```sh
kubectl get workspace -A
kubectl get workspace -n <namespace> <name> -o yaml
kubectl logs -n dwpk-system deployment/<gateway-deployment-name>
```

Two easy mistakes:

- Wrong SSH username. Use the workspace name, not your email.
- Duplicate workspace names with the same authorized key across namespaces. `ResolveWorkspaceTargetByNameAndPublicKey` rejects ambiguous matches.

## VS Code Remote-SSH fails after SSH login

The gateway supports loopback forwarding through `pods/portforward`, but the image still needs userland tools for VS Code server bootstrap.

Check the image contract:

- shell
- `curl` or `wget`
- `tar`
- `gzip`

If the workspace shell opens but VS Code fails, test those tools inside the pod.

## UI login redirect loop

Check the UI config and cookies.

```sh
kubectl logs -n dwpk-system deployment/<ui-deployment-name>
kubectl describe deployment -n dwpk-system <ui-deployment-name>
```

Common causes:

- `DWPK__UI_COOKIE_SECURE=true` while you test over plain HTTP
- Provider configured without `redirectURL` and without `DWPK__UI_BASE_URL`
- Wrong callback URL registered in the OAuth2 provider
- No `UserSpace` matches the provider email, so login is denied and the browser is sent back through the login flow
- Browser blocks the session cookie because the external URL does not match the way you exposed the service

Cookie names to inspect:

- `dwpk_ui_session`
- `dwpk_ui_login_state`
- `dwpk_ui_login_next`

## UI catalog or admin page returns 403

Catalog and workspace actions run with a minted token for the `session` ServiceAccount in your namespace (`session-readonly` for a read-scoped API token). A `403` usually means the RBAC for that service account does not allow the request.

Expected cases:

- Normal users can create and manage workspaces only in their own namespace.
- `/admin/users` and `/admin/quota` are blocked by default.
- `WorkspaceImage` entries hidden from the catalog often mean `use` permission is missing for that image.

## Logs tab is empty or refuses

`No output yet` is not a fault. It means the container is running and has written
nothing to stdout or stderr - common for a workspace whose entrypoint just sleeps.
Write something and re-check:

```sh
kubectl exec <pod> -n <namespace> -c workspace -- sh -c 'echo hi > /proc/1/fd/1'
```

`No pod is running` means the workspace is suspended. `status.podName` is empty,
and there is nothing to tail. Start it.

`Logs are unavailable` prints the API server's own message underneath. Confirm the
grant against the identity the browser actually uses - the per-namespace `session`
ServiceAccount, not the human:

```sh
kubectl auth can-i get pods/log \
  --as=system:serviceaccount:<namespace>:session -n <namespace>
```

`no` here means the per-user ClusterRole is missing its `pods`/`pods/log` rule
(`buildOwnerClusterRole` in `internal/controller/userspace_objects.go`). Asking
with `--as=<email>` instead will answer `yes` and tell you nothing about the UI.

## Terminal freezes mid-session

If the connection chip still reads `Connected` but nothing echoes, the socket is
open and the shell is gone. If it reads `Disconnected`, use the `Reconnect`
button rather than reloading the page.

A terminal that used to die every three seconds was a different bug, fixed: the
status poll swapped the whole card, terminal included. If it reappears, check
that nothing has moved the terminal markup back inside `WorkspaceStatusCard` -
`TestPolledFragmentExcludesTheTerminal` guards exactly that.

## Deleted a workspace and the storage is still billed

Expected. Deleting a `Workspace` removes the StatefulSet through its owner
reference, but the home PVC comes from a `volumeClaimTemplate` and no
`persistentVolumeClaimRetentionPolicy` is set, so Kubernetes keeps it:

```sh
kubectl get pvc -n <namespace>          # home-<workspace-name>-0 will still be Bound
kubectl delete pvc home-<workspace-name>-0 -n <namespace>
```

The UI's delete confirmation says this before the click. Recreating a workspace
of the same name does not reattach the old volume.

## `make test` or envtest setup fails

The Makefile derives envtest versions from `go.mod`.

Relevant lines:

- `ENVTEST_VERSION` comes from the `sigs.k8s.io/controller-runtime` module version
- `ENVTEST_K8S_VERSION` comes from the `k8s.io/api` module version, then `sed` reduces it to `1.<minor>`

Failure modes called out by the Makefile itself:

- controller-runtime replace with no tag, set `ENVTEST_VERSION` manually
- `k8s.io/api` replace with no tag, set `ENVTEST_K8S_VERSION` manually

Useful commands:

```sh
make setup-envtest
make test
```

If `setup-envtest` fails, export the version variables explicitly and rerun.

## Helm install fails

Check these first:

- cert-manager is installed and ready
- release namespace is `dwpk-system`
- gateway and UI image repositories and tags point to real images
- `rbac.namespaced=false` when gateway or UI are enabled
- no existing CRD ownership conflict from a previous install path

Useful commands:

```sh
helm show values oci://ghcr.io/devops-ia/helm-dwpk/dwpk --version <chart-version>
helm status dwpk -n dwpk-system
kubectl get events -n dwpk-system --sort-by=.lastTimestamp
```

If you're iterating on the chart itself rather than installing a release, `helm lint` against a
local clone of [devops-ia/helm-dwpk](https://github.com/devops-ia/helm-dwpk) instead.

Specific gotchas from current repo state:

- `make helm-deploy` only sets manager image values. It does not set gateway or UI images.
- `crd.keep=true` leaves CRDs behind on uninstall, which can confuse a later reinstall.
- All three images (manager, gateway, UI) are built and published by `.github/workflows/github-release.yml` on every tagged release; a chart install with no image overrides pulls the tag matching the chart's `appVersion`.

## Raw manifest install works, but SSH or UI is missing

That is expected. `dist/install.yaml` comes from `config/default` and installs only the operator stack. Use the Helm chart for the full platform.

## `idleTimeout` does nothing

This is a current implementation gap.

The API has `Workspace.spec.idleTimeout`, and the gateway updates `status.lastActivityTime`, but there is no `IdleReconciler` in the repository. Workspaces do not auto-stop on idle yet.
