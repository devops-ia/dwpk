package gateway

import "testing"

// The pod exec subresource has no Env field and never runs a login shell, so
// $HOME is unset unless shellCmd exports it - VS Code's remote-server install
// resolves its install path from $HOME and fails on an empty one.
func TestShellCmdExportsHomeWhenKnown(t *testing.T) {
	got := shellCmd("xterm", true, "/home/dev", "")
	want := `export SHELL="$(command -v bash || command -v sh)"; export HOME="/home/dev"; cd "$HOME" 2>/dev/null || true; export TERM="xterm"; exec "$SHELL" -l`
	if got[2] != want {
		t.Fatalf("shellCmd() = %q, want %q", got[2], want)
	}
}

func TestShellCmdFallsBackWhenHomeUnknown(t *testing.T) {
	got := shellCmd("xterm", false, "", "ls -la")
	want := `export SHELL="$(command -v bash || command -v sh)"; cd ${HOME:-/root} 2>/dev/null || true; ls -la`
	if got[2] != want {
		t.Fatalf("shellCmd() = %q, want %q", got[2], want)
	}
}

// $SHELL has to be set even for a one-off "exec" command, not just the bare
// interactive shell case - VS Code's remote-server install request runs
// through this branch, and the server process it starts inherits this
// environment for the lifetime of the connection.
func TestShellCmdExportsShellForExecCommands(t *testing.T) {
	got := shellCmd("", false, "/home/dev", "bash install.sh")
	want := `export SHELL="$(command -v bash || command -v sh)"; export HOME="/home/dev"; cd "$HOME" 2>/dev/null || true; bash install.sh`
	if got[2] != want {
		t.Fatalf("shellCmd() = %q, want %q", got[2], want)
	}
}
