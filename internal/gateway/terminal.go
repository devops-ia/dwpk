package gateway

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
)

type TerminalRequest struct {
	WorkspaceKey types.NamespacedName
	Term         string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Sizes        remotecommand.TerminalSizeQueue
}

func (s *Server) OpenTerminal(ctx context.Context, req TerminalRequest) error {
	workspace, err := s.getWorkspace(ctx, req.WorkspaceKey)
	if err != nil {
		return err
	}
	if !workspaceIsReady(workspace) {
		return fmt.Errorf("workspace %s is not ready yet (phase: %s)", workspace.Name, workspace.Status.State)
	}
	// Best-effort, both here and on the way out. This stamps
	// status.lastActivityTime for future idle detection; it is bookkeeping, and
	// bookkeeping must not be able to kill the thing it is keeping book on.
	//
	// It failed hard before, which broke the browser terminal outright: the UI
	// runs this code with the *user's* forwarded token (SPEC §8.1 - the UI holds
	// no rights of its own), and a user has only `get` on workspaces/status. The
	// SSH path did not notice because the gateway's own ServiceAccount may patch
	// it. Granting users write access to a status subresource to fix that would
	// have widened RBAC for a field nothing currently reads.
	s.recordActivity(ctx, req.WorkspaceKey)
	defer s.recordActivity(ctx, req.WorkspaceKey)

	target, err := s.resolvePodTarget(ctx, workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace pod: %w", err)
	}
	exitCode := s.execWithIO(ctx, target, execIO{
		Stdin:  req.Stdin,
		Stdout: req.Stdout,
		Stderr: req.Stderr,
	}, shellCmd(req.Term, true, target.HomePath, ""), true, req.Sizes)
	if exitCode != 0 {
		return fmt.Errorf("workspace terminal exited with status %d", exitCode)
	}
	return nil
}
