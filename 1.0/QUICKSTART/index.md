# Quick start

The fastest path from an empty cluster to an SSH session inside a workspace. This runs the manager, gateway, and UI locally against your kubeconfig — good for trying dwpk out or developing against it. For a real deployment, see [Installation](https://devops-ia.github.io/dwpk/1.0/INSTALLATION/index.md).

## Prerequisites

- A Kubernetes cluster you can reach with `kubectl` (OrbStack, kind, or minikube are all fine)
- Go 1.24.6+ and `kubectl` on your machine
- [cert-manager](https://cert-manager.io/) installed in the cluster — the manager's webhook needs it to issue its TLS certificate:

```
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.21.1/cert-manager.yaml
kubectl wait --for=condition=Available -n cert-manager deployment --all --timeout=120s
```

## 1. Install the CRDs

```
git clone https://github.com/devops-ia/dwpk.git
cd dwpk
make install
```

## 2. Run the three components

Each in its own terminal, against your current kubeconfig:

```
go run ./cmd/manager
go run ./cmd/gateway
DWPK__UI_KUBECONFIG=~/.kube/config DWPK__UI_LISTEN_ADDRESS=:8080 go run ./cmd/ui
```

## 3. Create a catalog entry, a user, and a workspace

```
kubectl apply -k config/samples/
```

Check that the sample `WorkspaceImage` matches your cluster before this step — its `placement.nodeSelector` has to match a real node label, or the workspace pod stays `Pending`.

## 4. Connect

Once `kubectl get workspace` shows `status.phase: Running`, connect over SSH with the key you set in the sample `UserSpace` (the sample workspace is named `dev`):

```
ssh dev@localhost -p 2222
```

Or open the UI at `http://localhost:8080` and use the browser terminal or the VS Code Remote-SSH deep link from the workspace's detail page.

## Next steps

- [Installation](https://devops-ia.github.io/dwpk/1.0/INSTALLATION/index.md) — the full Helm-based, production-shaped install (OAuth2 login, TLS, ingress)
- [User guide](https://devops-ia.github.io/dwpk/1.0/USER_GUIDE/index.md) — using the marketplace UI day to day
- [Administration](https://devops-ia.github.io/dwpk/1.0/ADMINISTRATION/index.md) — curating the catalog and provisioning users
