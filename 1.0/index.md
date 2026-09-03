# dwpk

## What is dwpk?

**dwpk (Development Workspace Platform for Kubernetes)** is a self-service platform for running persistent developer environments on your own Kubernetes cluster. A cluster admin curates a catalog of container images; developers pick one, click create, and get a namespaced, quota-bound workspace with a persistent home directory that they reach over SSH, VS Code Remote-SSH, or a terminal in the browser.

dwpk is not a hosted SaaS and it does not run your code on somebody else's infrastructure. It is an operator: three Go binaries you deploy into your own cluster, reconciling a small set of Kubernetes custom resources into namespaces, quotas, RBAC, and StatefulSets. There is no external database - control-plane state lives entirely in Kubernetes objects, so the platform inherits your cluster's own HA, backup, and audit story instead of inventing a second one.

## Why dwpk?

Most "cloud dev environment" products put your code and your identities on somebody else's cluster and bill per seat. dwpk inverts that: the environments run where your infrastructure already runs, access control is Kubernetes RBAC rather than a vendor's own authorization system, and the whole platform is three stateless-ish binaries and a handful of CRDs - nothing to run beyond the cluster you already operate.

That trade-off shapes the rest of the design:

- **Kubernetes-native state.** `WorkspaceImage`, `UserSpace`, `Workspace`, and `ImageRegistry` are ordinary CRDs. `kubectl get workspace` works. Backup is your existing etcd/cluster backup.
- **Bring your own identity.** Sign-in delegates to Entra ID, Google, GitLab, Keycloak, or GitHub - no separate user database to provision or leak.
- **No standing privilege.** The web UI never holds cluster-wide write access; every request mints a short-lived, namespace-scoped Kubernetes token for the signed-in person (§7.7/§8.1 of the design spec), so it is a thin, revocable pass-through rather than a security boundary of its own.

## Quick start

```
git clone https://github.com/devops-ia/dwpk.git
cd dwpk
make install                                 # CRDs into your current kubeconfig's cluster
go run ./cmd/manager & go run ./cmd/gateway   # run locally against that kubeconfig
kubectl apply -k config/samples/              # a sample catalog entry, user, and workspace
```

The full walkthrough, including the cert-manager prerequisite and connecting over SSH, is in [Quick start](https://devops-ia.github.io/dwpk/1.0/QUICKSTART/index.md). For a real deployment, see [Installation](https://devops-ia.github.io/dwpk/1.0/INSTALLATION/index.md).

## How it works

dwpk is three cooperating binaries sharing one Kubernetes API surface:

```
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

- **`cmd/manager`** is a Kubebuilder operator. It reconciles `WorkspaceImage` (catalog entries), `UserSpace` (one per person: namespace, quota, RBAC), `Workspace` (a running session backed by a single-replica `StatefulSet`), and `ImageRegistry` (polls an external registry, AWS ECR today, and syncs matching images into the catalog automatically).
- **`cmd/gateway`** is a stateless SSH gateway. It authenticates an incoming public key against the `Workspace` that trusts it, then bridges the session in over `pods/exec` and `pods/portforward` - the workspace image itself never runs `sshd`.
- **`cmd/ui`** is the marketplace web UI: Go, `templ`, and htmx, no SPA and no JS build step.

Full detail, including the RBAC model and the controller reconcile flows, is in [Architecture](https://devops-ia.github.io/dwpk/1.0/ARCHITECTURE/index.md).

## Features

- **Self-service catalog.** Admins curate `WorkspaceImage` entries (or sync them automatically from a container registry); users pick one, set CPU/memory/GPU, and create a workspace bound to their own quota.
- **Multiple ways in.** SSH, a VS Code Remote-SSH deep link, and an in-browser terminal, all through the same gateway and the same authorized keys.
- **Five identity providers.** Entra ID, Google, GitLab, Keycloak, and GitHub, with optional group-to-role mapping so an identity provider's own groups can grant admin access.
- **Per-user isolation.** One namespace per person, with a `ResourceQuota`, `LimitRange`, and `NetworkPolicy` the manager creates and enforces - never hand-provisioned.
- **Logs and events, not metrics.** Pod logs and Kubernetes events are read with the requesting user's own token, so access follows the same RBAC as everything else.
- **Git over SSH.** Users can register their own private git SSH keys, mounted into their workspace and never visible to the platform in plaintext.
- **Read-scoped API tokens.** A user can mint a full-access or read-only bearer token for scripting against the REST API, each backed by its own Kubernetes ServiceAccount.
- **No external database.** Every piece of platform state - catalog, quota, session, even local password logins for demos - is a Kubernetes object.

## Documentation map

- [Quick start](https://devops-ia.github.io/dwpk/1.0/QUICKSTART/index.md) - zero to a running workspace
- [Architecture](https://devops-ia.github.io/dwpk/1.0/ARCHITECTURE/index.md) - components, CRDs, reconcile flows, RBAC model
- [Gateway on AWS](https://devops-ia.github.io/dwpk/1.0/GATEWAY_AWS/index.md) - exposing SSH with the AWS Load Balancer Controller (NLB, not ALB)
- [Installation](https://devops-ia.github.io/dwpk/1.0/INSTALLATION/index.md) - Helm (OCI) and raw-manifest installs, OAuth2 provider setup
- [Administration](https://devops-ia.github.io/dwpk/1.0/ADMINISTRATION/index.md) - curating the catalog, provisioning users, day-2 operations
- [User guide](https://devops-ia.github.io/dwpk/1.0/USER_GUIDE/index.md) - using the marketplace UI
- [API reference](https://devops-ia.github.io/dwpk/1.0/API_REFERENCE/index.md) - CRDs and the REST API
- [Environment variables](https://devops-ia.github.io/dwpk/1.0/ENVIRONMENT_VARIABLES/index.md) - every variable each binary reads
- [Troubleshooting](https://devops-ia.github.io/dwpk/1.0/TROUBLESHOOTING/index.md) - common failures and fixes

Development notes (building, testing, local dev loop) stay in the root [README](https://github.com/devops-ia/dwpk/blob/main/README.md); this docs set does not duplicate that.
