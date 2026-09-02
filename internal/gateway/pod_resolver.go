package gateway

import (
	"crypto/subtle"
	"fmt"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/crypto/ssh"
)

// WorkspaceTarget is the workspace authenticated for one SSH connection, plus
// the only pod the gateway may reach for it.
type WorkspaceTarget struct {
	Workspace    *dwpkv1alpha1.Workspace
	PodNamespace string
	PodName      string
}

// ResolveWorkspaceTargetByNameAndPublicKey finds the one Workspace whose SSH
// identity matches the requested username and which trusts the offered key.
//
// The comparison is against workspacepkg.SSHUser - the same function the
// controller uses to publish status.endpoint - rather than against ws.Name.
// A bare workspace name is unique only inside its namespace, so two users with
// a workspace called "dev" used to be indistinguishable here.
func ResolveWorkspaceTargetByNameAndPublicKey(
	workspaceName string,
	publicKey ssh.PublicKey,
	workspaces []dwpkv1alpha1.Workspace,
) (WorkspaceTarget, error) {
	if workspaceName == "" {
		return WorkspaceTarget{}, fmt.Errorf("missing SSH username")
	}

	var matches []WorkspaceTarget

	for i := range workspaces {
		ws := &workspaces[i]
		if workspacepkg.SSHUser(ws) != workspaceName || !workspaceAuthorizesKey(ws, publicKey) {
			continue
		}
		matches = append(matches, WorkspaceTarget{
			Workspace:    ws,
			PodNamespace: ws.Namespace,
			PodName:      workspacepkg.PodName(ws),
		})
	}

	switch len(matches) {
	case 0:
		return WorkspaceTarget{}, fmt.Errorf("no Workspace named %q matched public key", workspaceName)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Workspace.Namespace+"/"+match.Workspace.Name)
		}
		return WorkspaceTarget{}, fmt.Errorf("public key matched multiple Workspaces named %q: %s", workspaceName, strings.Join(names, ", "))
	}
}

// ResolveWorkspaceTargetByPublicKey finds the one Workspace that trusts the
// offered public key and derives the StatefulSet pod name from that object.
func ResolveWorkspaceTargetByPublicKey(
	publicKey ssh.PublicKey,
	workspaces []dwpkv1alpha1.Workspace,
) (WorkspaceTarget, error) {
	var matches []WorkspaceTarget

	for i := range workspaces {
		ws := &workspaces[i]
		if !workspaceAuthorizesKey(ws, publicKey) {
			continue
		}
		matches = append(matches, WorkspaceTarget{
			Workspace:    ws,
			PodNamespace: ws.Namespace,
			PodName:      workspacepkg.PodName(ws),
		})
	}

	switch len(matches) {
	case 0:
		return WorkspaceTarget{}, fmt.Errorf("no Workspace matched public key")
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Workspace.Namespace+"/"+match.Workspace.Name)
		}
		return WorkspaceTarget{}, fmt.Errorf("public key matched multiple Workspaces: %s", strings.Join(names, ", "))
	}
}

func workspaceAuthorizesKey(ws *dwpkv1alpha1.Workspace, publicKey ssh.PublicKey) bool {
	for _, line := range ws.Spec.SSHAuthorizedKeys {
		authorizedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		if publicKeysEqual(publicKey, authorizedKey) {
			return true
		}
	}
	return false
}

func publicKeysEqual(left, right ssh.PublicKey) bool {
	leftBytes := left.Marshal()
	rightBytes := right.Marshal()
	if len(leftBytes) != len(rightBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}
