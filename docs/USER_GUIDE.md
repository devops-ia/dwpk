# User guide

## Before you start

A cluster admin must create your `UserSpace` first. The UI login flow resolves your provider email against `UserSpace.spec.owner`. If there is no match, login is denied.

## Sign in

Open the UI `/login` page. Current provider buttons in the code:

- Entra ID
- Google
- GitLab
- Keycloak
- GitHub

Flow:

1. Pick your provider.
2. Complete the provider login.
3. Return to dwpk.
4. Land on the catalog page if your email matches an existing `UserSpace`.

The UI session cookie is separate from your workspace lifecycle. Logging out does not stop a running workspace.

## Getting started

Your first sign-in lands on a four-step walkthrough: add an SSH key, browse the catalog, start a
workspace, finish. It is optional. Navigate away whenever you like — nothing is blocked while it is
unfinished, and **Get started** stays in the sidebar until you press Finish, which is also how you
get back to it. After that it disappears and every sign-in goes straight to your overview.

If you dismiss it and later want it again, `/onboarding` still renders. Pressing Finish a second time
changes nothing.

## Browse the catalog

The catalog page is `/` after login.

What you can do there:

- Search by free text with `q`
- Filter by tag with `tag`
- Show deprecated entries with `deprecated=1`

The backend checks `use` permission on each `WorkspaceImage` with `SelfSubjectAccessReview`. If you are not allowed to use an image, it does not appear in the catalog.

## Create a workspace

Open `Create workspace` from a catalog card, or go to `/new?image=<workspaceimage-name>`.

Fields on the form:

- Name, unique among your workspaces
- Resources: CPU and memory as a minimum and a maximum, then GPU and storage
- SSH public key

Notes from the current UI:

- There are no size presets. You type the resources you want, within your quota.
- CPU and memory have a min and a max - a request and a limit. The minimum is
  what is reserved for you and counted against your quota; the maximum is what
  you may burst to.
- GPU and storage take one value each. A PVC has a single size, and Kubernetes
  requires a GPU request and limit to be equal.
- The form checks your remaining quota as you type, and the admission webhook
  checks it again on submit. The second check is the one that counts.
- Storage cannot be changed afterwards: a PVC cannot shrink. Everything else
  needs the workspace recreated, which is why the form is worth a second look.
- The SSH key field remembers the last key in browser `localStorage` under `dwpk.lastSshPublicKey`.
- The UI submits a normal `Workspace` object to the Kubernetes API. Validation errors come back from the API server.

## Wait for it to start

After create, the UI redirects to `/w/<workspace-name>`.

The page shows:

- `status.phase`
- Endpoint
- Conditions summary
- The full SSH command
- A VS Code Remote-SSH deep link
- Start and stop buttons

While the workspace is not settled, the page polls `/w/<workspace-name>/status` every 3 seconds.

## Connect with SSH

The command shown on the page is:

```text
ssh <workspace-name>@<gateway-host>
```

The SSH username is the workspace name. Authentication is by public key. The gateway accepts the session only when the offered public key matches one of the entries in `Workspace.spec.sshAuthorizedKeys`.

If your client prompts about a new host key often, ask your admin whether the gateway is using an ephemeral host key instead of a Secret-backed fixed key.

## Connect with VS Code Remote-SSH

The UI builds this link format:

```text
vscode://vscode-remote/ssh-remote+<workspace-name>@<gateway-host>/
```

Click `Open in VS Code`, or paste the same SSH target into Remote-SSH manually.

Catalog image contract for VS Code in this repo:

- A shell, `bash` or `sh`
- `curl` or `wget`
- `tar`
- `gzip`

If VS Code fails during server bootstrap, the image may be missing one of those tools.

## Use the browser terminal

The workspace page has a `Terminal` tab. Current state in the repo:

- Backend websocket path exists at `/w/<workspace-name>/terminal/ws`
- The frontend is a textarea fallback, not a full terminal emulator
- Press `Enter` to send the current line
- Use `Shift+Enter` for a newline in the textarea

This terminal reuses the same gateway exec path as SSH.

A chip above the output says whether you are connected. If the connection drops

- the pod restarted, the workspace was stopped, the network moved - it turns red
and a `Reconnect` button appears. The tab shows `No pod to attach to` when the
workspace is suspended, because there is nothing to attach to yet.

## Read the workspace logs

Beside `Terminal` is a `Logs` tab. It tails the last 200 lines the workspace
container wrote to stdout or stderr, refreshed every five seconds.

It reads with your own token, so you see the logs of workspaces in your own
namespace and nobody else's. A refusal shows the API server's own message rather
than a generic error.

Four things it can say, and what each means:

- Log lines - the container is writing output.
- `No output yet` - the container is running and has written nothing. Not a fault.
- `No pod is running` - the workspace is stopped. Start it first.
- `Logs are unavailable` - with the reason underneath, usually an RBAC message.

There is no metrics view. Resource numbers live on the quota screen.

## Start and stop a workspace

The page has `Start` and `Stop` buttons.

What they do:

- `Start` patches `spec.running=true`
- `Stop` patches `spec.running=false`

What that means in the cluster:

- `true` scales the workspace `StatefulSet` to one replica
- `false` scales it to zero replicas
- The home PVC stays in place either way

Stopping a workspace is different from logging out:

- Logout clears the UI session only.
- Stop changes the workspace state in Kubernetes.

## What persists

Your home directory lives on the workspace PVC. Stop and start keep that data.

Deleting a workspace is immediate and cannot be undone. Its pod and its SSH
endpoint go with it.

The home volume is a separate object - the PVC named `home-<workspace-name>-0` -
and Kubernetes never removes it on its own. The delete dialog therefore asks:
**Delete the home volume as well** is ticked by default, and untick it to keep
the data. Deleting the workspace with the box unticked leaves the volume behind,
unattached; creating a new workspace with the same name does not reattach it, so
remove it by hand when you no longer want it:

```sh
kubectl delete pvc home-<workspace-name>-0 -n <your-namespace>
```

Because the volume goes by default, the dialog asks you to type the workspace
name before it will delete anything. Deleting over the REST API never touches
the volume.

The repo also allows you to use Kubernetes resources in your namespace with the workspace service account, because that service account is bound to the built-in `edit` ClusterRole in your namespace.

## What you cannot do by default

The default session token does not grant cluster-wide admin rights.

That means:

- You cannot list other users' namespaces or workspaces.
- The `/admin/users` and `/admin/quota` pages usually return `403 Forbidden` for normal users.
- You only get the permissions already bound to the `workspace` ServiceAccount in your namespace.
