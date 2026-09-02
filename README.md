# dwpk

**Development Workspace Platform for Kubernetes.** Give every developer on your team a real,
persistent, browser- and SSH-reachable dev environment running on your own cluster, provisioned
from a catalog you control.

[![License](https://img.shields.io/github/license/devops-ia/dwpk)](./LICENSE)
[![Release](https://img.shields.io/github/v/release/devops-ia/dwpk)](https://github.com/devops-ia/dwpk/releases)
[![Docs](https://img.shields.io/badge/docs-devops--ia.github.io%2Fdwpk-blue)](https://devops-ia.github.io/dwpk/)

**[Full documentation →](https://devops-ia.github.io/dwpk/)**

## Why dwpk

Most "cloud dev environment" products ask you to run your code on somebody else's cluster and pay
per seat. dwpk is different: it's an operator you run on your own Kubernetes, so your workspaces,
your data, and your access control all stay where your infrastructure already lives.

A cluster admin picks which container images are on offer (a Python image, a Go image, whatever
your team ships against) and how much CPU, memory, and GPU each person is allowed. From there,
developers pick an image from the catalog, click create, and get a namespaced, quota-bound
environment with a persistent home directory. They reach it over SSH, VS Code Remote-SSH, or a
terminal in the browser, and sign in with whichever identity provider you already use: Entra ID,
Google, GitLab, Keycloak, or GitHub.

There's no external database and no separate control plane to run. State lives in Kubernetes
objects, the same way `Deployment` and `Service` state does, so the platform inherits your
cluster's HA, backup, and RBAC story instead of inventing its own.

## How it's built

dwpk is three Go binaries sharing one API.

`cmd/manager` is a [Kubebuilder](https://book.kubebuilder.io/) operator. It reconciles
`WorkspaceImage` (catalog entries), `UserSpace` (one per person: namespace, quota, RBAC), and
`Workspace` (a running session, backed by a single-replica `StatefulSet`).

`cmd/gateway` is a stateless SSH gateway. It checks an incoming public key against the `Workspace`
that trusts it, then bridges the session in over `pods/exec` and `pods/portforward`. The workspace
image itself never runs `sshd`.

`cmd/ui` is the marketplace web UI: Go, [`templ`](https://templ.guide), and htmx, with no SPA and
no JS build step. It runs OAuth2 login, then mints a short-lived Kubernetes token per request
through `TokenRequest` instead of holding standing cluster permissions of its own.

See [Architecture](https://devops-ia.github.io/dwpk/ARCHITECTURE/) for the complete design: the CRDs, the controller reconcile flows, the
security model, and the rationale behind each of them.

## Quick start

```sh
git clone https://github.com/devops-ia/dwpk.git
cd dwpk
make install                                 # CRDs into your current kubeconfig's cluster
go run ./cmd/manager & go run ./cmd/gateway   # run locally against that kubeconfig
kubectl apply -k config/samples/              # a sample catalog entry, user, and workspace
```

The full walkthrough, including cert-manager setup and connecting over SSH, is in
[Quick start](https://devops-ia.github.io/dwpk/QUICKSTART/).

## Installing for real

The chart lives in a separate repo, [devops-ia/helm-dwpk](https://github.com/devops-ia/helm-dwpk),
released independently of this one and published as an OCI artifact on `ghcr.io`:

```sh
helm upgrade --install dwpk oci://ghcr.io/devops-ia/helm-dwpk/dwpk \
  --version <chart-version> \
  -n dwpk-system \
  --create-namespace
```

Component images default to the ones this repo publishes on every release, so no image overrides
are needed unless you're running your own build. If you're turning on the UI, create the OAuth2
client secret first and point `ui.oauth.existingSecret` at it. Client secrets belong in a
Kubernetes `Secret`, not in `values.yaml`. The full install guide, including every provider's setup
and the raw-manifest alternative, is at
[Installation](https://devops-ia.github.io/dwpk/INSTALLATION/).

## Local development

```sh
make manifests generate   # after touching api/v1alpha1 or a +kubebuilder marker
make lint-fix              # gofmt + golangci-lint --fix
make test                  # unit tests plus envtest, against a real API server
```

`make test` covers `internal/controller` and `internal/webhook` with envtest, and
`internal/gateway` with both fake-client unit tests and an envtest integration suite — nothing in
this codebase mocks the Kubernetes API.

## Documentation

| | |
| --- | --- |
| [Quick start](https://devops-ia.github.io/dwpk/QUICKSTART/) | Zero to a running workspace |
| [Installation](https://devops-ia.github.io/dwpk/INSTALLATION/) | Helm and raw-manifest installs, OAuth2 setup |
| [Architecture](https://devops-ia.github.io/dwpk/ARCHITECTURE/) | Components, CRDs, reconcile flows |
| [Administration](https://devops-ia.github.io/dwpk/ADMINISTRATION/) | Managing the catalog and users |
| [User guide](https://devops-ia.github.io/dwpk/USER_GUIDE/) | Using the marketplace UI |
| [API reference](https://devops-ia.github.io/dwpk/API_REFERENCE/) | CRDs and the REST API |
| [Troubleshooting](https://devops-ia.github.io/dwpk/TROUBLESHOOTING/) | Common failures and fixes |

## Contributing

Read [`CONTRIBUTING.md`](./CONTRIBUTING.md) before opening a pull request, `api/v1alpha1`
is a shared contract across the operator, the gateway, and the UI, and commit messages
follow [Conventional Commits](https://www.conventionalcommits.org/) because
semantic-release reads them to cut versions.

## License

Apache License 2.0, copyright DevOps IA. See [`LICENSE`](./LICENSE).
