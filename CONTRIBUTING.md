# Contributing to dwpk

dwpk is a Kubernetes operator, an SSH gateway and a web UI for running development
workspaces in a cluster. This page covers how to get a change merged and released.

## Reporting a bug

Search [the issues](https://github.com/devops-ia/dwpk/issues) first. If nothing matches,
[open one](https://github.com/devops-ia/dwpk/issues/new) with what you did, what you
expected, and what happened instead. For anything involving the cluster, include the
operator version (it is logged at startup and stamped into the binary), the output of
`kubectl get workspace <name> -o yaml`, and the manager logs around the failure.

## Proposing a feature

Open an issue before writing the code. Larger changes touch the CRDs, and
`api/v1alpha1` is a shared contract — a field added there ripples into the controller, the
webhooks, the Helm chart and the REST API.

## Local development

```sh
make generate manifests   # after any api/v1alpha1 change
make test                 # unit tests plus envtest
make lint                 # golangci-lint
make build-all            # all three binaries into bin/
```

Run `make generate manifests` and `make test` before opening a pull request. "It compiles"
is not the bar.

A few rules the reviewers will hold you to:

- Reconcilers delegate. `Reconcile` orchestrates; it never builds a PodSpec inline.
- Desired-state construction is pure functions — take the CRs, return the object. No
  client, no context, testable at a table.
- Validation belongs at admission, not in the controller. Prefer CRD defaults, then CEL
  `x-kubernetes-validations`, then `ValidatingAdmissionPolicy`, and only then a webhook.
  Only a cross-object read justifies a webhook.
- No mocking the Kubernetes API. `envtest` exists so you do not have to.
- Every branch that writes to the cluster has a test.

Changing user-facing behaviour in `internal/ui/` means updating the REST API in the same
pull request: `internal/ui/api.go` / `api_resources.go`, **both** copies of the OpenAPI spec
(`docs/openapi.yaml` and its embedded twin `internal/ui/assets/openapi.yaml`, which a test
holds byte-identical), and `docs/API_REFERENCE.md`.

## The Helm chart lives elsewhere

The chart is [devops-ia/helm-dwpk](https://github.com/devops-ia/helm-dwpk), at
`charts/dwpk`. Its CRDs and manager ClusterRole are generated from this repository, so do
not hand-edit them there — a workflow opens a sync pull request whenever `api/v1alpha1`
changes. To do it by hand:

```sh
make sync-chart-crds HELM_CHART_DIR=../helm-dwpk/charts/dwpk
```

## Commit messages and releases

Releases are automatic. Merging to `main` runs semantic-release, which reads the commit
messages, decides the next version, tags it, and publishes the binaries and the three
container images. That makes the message format load-bearing rather than cosmetic:

```
<type>(<optional scope>): <subject>
```

`fix` gives a patch release, `feat` a minor one, and a `BREAKING CHANGE:` footer a major
one. `docs`, `ci` and `chore` release nothing. Use the imperative mood, start the subject
with a capital letter, and keep the first line under 72 characters. Reference issues freely
after it.

Pull request titles are checked against the same rules, because a squash merge takes its
commit message from the title.

## YAML style

Two-space indentation, hyphens for list items, no trailing whitespace, no tabs. Templates
and manifests follow the conventions already in `config/` — match the file you are editing.
