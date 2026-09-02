package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	workspacepkg "github.com/devops-ia/dwpk/internal/workspace"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	permissionWorkspaceNamespace = "workspaceNamespace"
	permissionWorkspaceName      = "workspaceName"
)

type Server struct {
	WorkspaceClient ctrlclient.Client
	KubeClient      kubernetes.Interface
	RESTConfig      *rest.Config
	HostSigner      ssh.Signer
}

type podTarget struct {
	Namespace string
	Name      string
	// HomePath is the workspace container's configured WorkingDir - the home
	// PVC's mount path (WorkspaceImage.Spec.HomePath). The pod exec subresource
	// has no Env field and does not run a login shell, so nothing populates
	// $HOME for an exec'd process; shellCmd exports it explicitly from this.
	HomePath string
}

type execIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func NewServer(
	workspaceClient ctrlclient.Client,
	kubeClient kubernetes.Interface,
	restConfig *rest.Config,
	hostSigner ssh.Signer,
) *Server {
	return &Server{
		WorkspaceClient: workspaceClient,
		KubeClient:      kubeClient,
		RESTConfig:      restConfig,
		HostSigner:      hostSigner,
	}
}

// maxConcurrentConnections bounds how many unauthenticated TCP connections the
// accept loop will service at once. Without it, a remote party can open
// connections without ever offering a key and exhaust the pod's file
// descriptors and memory - MaxAuthTries limits attempts per connection, this
// limits how many connections exist at all.
const maxConcurrentConnections = 256

func (s *Server) ListenAndServe(ctx context.Context, listenAddress string) error {
	logger := log.FromContext(ctx).WithName("gateway").WithValues("listenAddress", listenAddress)
	ctx = log.IntoContext(ctx, logger)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: s.publicKeyCallback,
		MaxAuthTries:      6,
	}
	cfg.AddHostKey(s.HostSigner)

	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddress, err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	connSlots := make(chan struct{}, maxConcurrentConnections)

	logger.Info("Starting SSH gateway")
	for {
		netConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Error(err, "Failed to accept SSH connection")
			continue
		}
		select {
		case connSlots <- struct{}{}:
			go func() {
				defer func() { <-connSlots }()
				s.serve(ctx, netConn, cfg)
			}()
		default:
			logger.V(1).Info("Rejecting SSH connection: at capacity",
				"client", netConn.RemoteAddr().String(), "limit", maxConcurrentConnections)
			_ = netConn.Close()
		}
	}
}

func (s *Server) publicKeyCallback(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	logger := log.Log.WithName("gateway").WithValues(
		"client", conn.RemoteAddr().String(),
		"user", conn.User(),
	)

	workspaces := &dwpkv1alpha1.WorkspaceList{}
	if err := s.WorkspaceClient.List(context.Background(), workspaces); err != nil {
		logger.Error(err, "Failed to list Workspaces during authentication")
		return nil, fmt.Errorf("list Workspaces: %w", err)
	}

	target, err := ResolveWorkspaceTargetByNameAndPublicKey(conn.User(), key, workspaces.Items)
	if err != nil {
		return nil, err
	}

	return &ssh.Permissions{Extensions: map[string]string{
		permissionWorkspaceNamespace: target.Workspace.Namespace,
		permissionWorkspaceName:      target.Workspace.Name,
	}}, nil
}

func (s *Server) serve(ctx context.Context, netConn net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(netConn, cfg)
	if err != nil {
		// Debug, not error. The kubelet's tcpSocket probe opens this port every
		// ten seconds, speaks no SSH and hangs up, so at error level a perfectly
		// healthy gateway prints two failures a minute for ever and anyone
		// reading the logs concludes it is broken. Port scanners do the same.
		// A key that is offered and rejected is logged separately, in
		// publicKeyCallback, and stays visible.
		log.FromContext(ctx).V(1).Info("SSH handshake did not complete",
			"client", netConn.RemoteAddr().String(), "reason", err.Error())
		return
	}
	defer func() { _ = conn.Close() }()

	workspaceKey, err := workspaceKeyFromPermissions(conn.Permissions)
	if err != nil {
		log.FromContext(ctx).Error(err, "Authentication succeeded without Workspace metadata")
		return
	}

	logger := log.FromContext(ctx).WithValues(
		"client", conn.RemoteAddr().String(),
		"user", conn.User(),
		"workspace", workspaceKey.Namespace+"/"+workspaceKey.Name,
	)
	ctx = log.IntoContext(ctx, logger)

	workspace, err := s.getWorkspace(ctx, workspaceKey)
	if err != nil {
		logger.Error(err, "Failed to reload authenticated Workspace")
		return
	}

	if err := s.touchWorkspaceActivity(ctx, workspaceKey); err != nil {
		logger.Error(err, "Failed to record session open")
	}
	defer func() {
		if err := s.touchWorkspaceActivity(ctx, workspaceKey); err != nil {
			logger.Error(err, "Failed to record session close")
		}
	}()

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		switch newChannel.ChannelType() {
		case "session":
			go s.session(ctx, newChannel, workspace.DeepCopy())
		case "direct-tcpip":
			go s.directTCPIP(ctx, newChannel, workspace.DeepCopy())
		default:
			logger.Info("Rejecting SSH channel", "type", newChannel.ChannelType())
			_ = newChannel.Reject(ssh.UnknownChannelType, newChannel.ChannelType())
		}
	}
}

func (s *Server) getWorkspace(ctx context.Context, key types.NamespacedName) (*dwpkv1alpha1.Workspace, error) {
	workspace := &dwpkv1alpha1.Workspace{}
	if err := s.WorkspaceClient.Get(ctx, key, workspace); err != nil {
		return nil, fmt.Errorf("get Workspace %s/%s: %w", key.Namespace, key.Name, err)
	}
	return workspace, nil
}

// recordActivity stamps the activity time and swallows a refusal.
//
// Whether it succeeds depends on who is asking: the gateway's ServiceAccount
// may patch a workspace status, a user's own session may not. Both are correct,
// and neither should stop a terminal from opening.
func (s *Server) recordActivity(ctx context.Context, key types.NamespacedName) {
	if err := s.touchWorkspaceActivity(ctx, key); err != nil {
		log.FromContext(ctx).V(1).Info("Could not record workspace activity",
			"workspace", key.String(), "reason", err.Error())
	}
}

func (s *Server) touchWorkspaceActivity(ctx context.Context, key types.NamespacedName) error {
	workspace, err := s.getWorkspace(ctx, key)
	if err != nil {
		return err
	}
	patch := ctrlclient.MergeFrom(workspace.DeepCopy())
	now := metav1.Now()
	workspace.Status.LastActivityTime = &now
	if err := s.WorkspaceClient.Status().Patch(ctx, workspace, patch); err != nil {
		return fmt.Errorf("patch Workspace status %s/%s: %w", key.Namespace, key.Name, err)
	}
	return nil
}

func workspaceKeyFromPermissions(permissions *ssh.Permissions) (types.NamespacedName, error) {
	if permissions == nil {
		return types.NamespacedName{}, fmt.Errorf("missing SSH permissions")
	}
	namespace := permissions.Extensions[permissionWorkspaceNamespace]
	name := permissions.Extensions[permissionWorkspaceName]
	if namespace == "" || name == "" {
		return types.NamespacedName{}, fmt.Errorf("missing Workspace metadata in SSH permissions")
	}
	return types.NamespacedName{Namespace: namespace, Name: name}, nil
}

func workspaceIsReady(workspace *dwpkv1alpha1.Workspace) bool {
	return workspace.Status.State == dwpkv1alpha1.WorkspaceStateRunning && workspace.Status.PodName != ""
}

func workspaceNotReadyMessage(workspace *dwpkv1alpha1.Workspace) string {
	phase := workspace.Status.State
	if phase == "" {
		phase = dwpkv1alpha1.WorkspaceStatePending
	}
	return fmt.Sprintf("Workspace %s is not ready yet (phase: %s). Try again later.\r\n", workspace.Name, phase)
}

func gatewayErrorMessage(err error) string {
	return fmt.Sprintf("Gateway could not open Workspace session: %v\r\n", err)
}

func (s *Server) resolvePodTarget(ctx context.Context, workspace *dwpkv1alpha1.Workspace) (podTarget, error) {
	podName := workspace.Status.PodName
	if podName == "" {
		podName = workspacepkg.PodName(workspace)
	}

	pod, err := s.KubeClient.CoreV1().Pods(workspace.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return podTarget{}, fmt.Errorf("get Pod %s/%s: %w", workspace.Namespace, podName, err)
	}
	if pod.Labels[dwpkv1alpha1.WorkspaceLabel] != workspace.Name {
		return podTarget{}, fmt.Errorf("pod %s/%s is not labeled for Workspace %q", pod.Namespace, pod.Name, workspace.Name)
	}
	return podTarget{Namespace: pod.Namespace, Name: pod.Name, HomePath: containerWorkingDir(pod)}, nil
}

// containerWorkingDir reads the workspace container's WorkingDir straight off
// the Pod we already fetched, rather than making a second API call for the
// WorkspaceImage that set it.
func containerWorkingDir(pod *corev1.Pod) string {
	for _, container := range pod.Spec.Containers {
		if container.Name == workspacepkg.ContainerName {
			return container.WorkingDir
		}
	}
	return ""
}

// sizeQueue feeds window-change events to the exec stream.
type sizeQueue struct {
	ch   chan remotecommand.TerminalSize
	once sync.Once
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

func (q *sizeQueue) push(cols, rows uint32) {
	select {
	case q.ch <- remotecommand.TerminalSize{Width: clampToUint16(cols), Height: clampToUint16(rows)}:
	default:
	}
}

// clampToUint16 saturates rather than wraps: an SSH client claiming a
// terminal wider than 65535 columns should get the widest size we can report,
// not the silently-wrapped-around value uint16(cols) would produce.
func clampToUint16(v uint32) uint16 {
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

func (q *sizeQueue) close() { q.once.Do(func() { close(q.ch) }) }

type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32
	Modes         string
}

type winChange struct{ Cols, Rows, Width, Height uint32 }

func (s *Server) session(ctx context.Context, newChannel ssh.NewChannel, workspace *dwpkv1alpha1.Workspace) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to accept session channel")
		return
	}

	if !workspaceIsReady(workspace) {
		_, _ = io.WriteString(channel, workspaceNotReadyMessage(workspace))
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{1}))
		_ = channel.Close()
		for request := range requests {
			_ = request.Reply(false, nil)
		}
		return
	}

	target, err := s.resolvePodTarget(ctx, workspace)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to resolve workspace pod")
		_, _ = io.WriteString(channel, gatewayErrorMessage(err))
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{1}))
		_ = channel.Close()
		for request := range requests {
			_ = request.Reply(false, nil)
		}
		return
	}

	var (
		tty   bool
		term  = "xterm-256color"
		sizes = &sizeQueue{ch: make(chan remotecommand.TerminalSize, 4)}
		start sync.Once
	)

	run := func(command []string) {
		start.Do(func() {
			go func() {
				exitCode := s.exec(ctx, target, channel, command, tty, sizes)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{exitCode}))
				sizes.close()
				_ = channel.Close()
			}()
		})
	}

	for request := range requests {
		switch request.Type {
		case "pty-req":
			var payload ptyReq
			if err := ssh.Unmarshal(request.Payload, &payload); err == nil {
				tty, term = true, payload.Term
				sizes.push(payload.Cols, payload.Rows)
			}
			_ = request.Reply(true, nil)
		case "window-change":
			var payload winChange
			if err := ssh.Unmarshal(request.Payload, &payload); err == nil {
				sizes.push(payload.Cols, payload.Rows)
			}
			_ = request.Reply(true, nil)
		case "env":
			_ = request.Reply(true, nil)
		case "shell":
			_ = request.Reply(true, nil)
			run(shellCmd(term, tty, target.HomePath, ""))
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			run(shellCmd(term, tty, target.HomePath, payload.Command))
		case "signal":
			_ = request.Reply(false, nil)
		default:
			log.FromContext(ctx).Info("Rejecting session request", "type", request.Type)
			_ = request.Reply(false, nil)
		}
	}

	sizes.close()
}

// shellCmd builds the command line the pod exec subresource runs. The exec
// API has no Env field and never runs a login shell, so neither $HOME nor
// $SHELL is set unless we export them ourselves.
//
// $SHELL matters beyond this one session: the "exec" case is also how VS
// Code's remote-server install command runs, and that server process keeps
// running afterward as a long-lived daemon inheriting whatever environment it
// was launched with. Its own integrated-terminal default-shell detection
// falls back to $SHELL (and, failing that, /etc/passwd - which containers
// commonly have no entry for, since they run as a numeric UID), so an unset
// $SHELL here is exactly why a later "New Terminal" in VS Code opened sh
// instead of bash. Exporting it now, with the same bash-if-present
// preference as the interactive shell below, fixes both.
func shellCmd(term string, tty bool, homePath, command string) []string {
	prelude := `export SHELL="$(command -v bash || command -v sh)"; cd ${HOME:-/root} 2>/dev/null || true;`
	if homePath != "" {
		prelude = fmt.Sprintf(`export SHELL="$(command -v bash || command -v sh)"; export HOME=%q; cd "$HOME" 2>/dev/null || true;`, homePath)
	}
	if tty {
		prelude += fmt.Sprintf(" export TERM=%q;", term)
	}
	if command == "" {
		return []string{"/bin/sh", "-c", prelude + ` exec "$SHELL" -l`}
	}
	return []string{"/bin/sh", "-c", prelude + " " + command}
}

func (s *Server) exec(
	ctx context.Context,
	target podTarget,
	channel ssh.Channel,
	command []string,
	tty bool,
	sizes *sizeQueue,
) uint32 {
	return s.execWithIO(ctx, target, execIO{
		Stdin:  channel,
		Stdout: channel,
		Stderr: channel.Stderr(),
	}, command, tty, sizes)
}

func (s *Server) execWithIO(
	ctx context.Context,
	target podTarget,
	stream execIO,
	command []string,
	tty bool,
	sizes remotecommand.TerminalSizeQueue,
) uint32 {
	request := s.KubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(target.Namespace).
		Name(target.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: workspacepkg.ContainerName,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    !tty,
			TTY:       tty,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewWebSocketExecutor(s.RESTConfig, http.MethodGet, request.URL().String())
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to create WebSocket exec executor")
		return 255
	}
	spdyExecutor, err := remotecommand.NewSPDYExecutor(s.RESTConfig, http.MethodPost, request.URL())
	if err == nil {
		executor, _ = remotecommand.NewFallbackExecutor(executor, spdyExecutor, func(error) bool { return true })
	}

	options := remotecommand.StreamOptions{Stdin: stream.Stdin, Stdout: stream.Stdout, Tty: tty}
	if tty {
		options.TerminalSizeQueue = sizes
	} else {
		options.Stderr = stream.Stderr
	}

	if err := executor.StreamWithContext(ctx, options); err != nil {
		var exitError interface{ ExitStatus() int }
		if errors.As(err, &exitError) {
			return uint32(exitError.ExitStatus())
		}
		log.FromContext(ctx).Error(err, "Workspace exec stream failed")
		return 255
	}
	return 0
}

// isLoopbackForwardTarget reports whether a direct-tcpip request targets the
// resolved workspace pod's own loopback, the only destination the gateway
// forwards to. Anything else would dial out from the gateway pod itself.
func isLoopbackForwardTarget(destAddr string) bool {
	switch destAddr {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

type tcpipForward struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func (s *Server) directTCPIP(ctx context.Context, newChannel ssh.NewChannel, workspace *dwpkv1alpha1.Workspace) {
	if !workspaceIsReady(workspace) {
		_ = newChannel.Reject(ssh.ConnectionFailed, workspaceNotReadyMessage(workspace))
		return
	}

	target, err := s.resolvePodTarget(ctx, workspace)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to resolve workspace pod for TCP forward")
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}

	var forward tcpipForward
	if err := ssh.Unmarshal(newChannel.ExtraData(), &forward); err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}

	// Only loopback is forwarded, and only into the resolved workspace pod: a
	// direct-tcpip request naming any other host would dial straight out of
	// the gateway pod's own network, turning a legitimate SSH session into an
	// open proxy onto the cluster's internal network (or a cloud metadata
	// endpoint) that RBAC never gets a chance to see.
	var upstream io.ReadWriteCloser
	if isLoopbackForwardTarget(forward.DestAddr) {
		upstream, err = s.portForward(target, forward.DestPort)
	} else {
		err = fmt.Errorf("direct-tcpip to %q refused: only the workspace's own loopback is forwarded", forward.DestAddr)
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to open direct-tcpip upstream", "destination", forward.DestAddr, "port", forward.DestPort)
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	go ssh.DiscardRequests(requests)

	go func() {
		_, _ = io.Copy(upstream, channel)
		_ = upstream.Close()
	}()
	_, _ = io.Copy(channel, upstream)
	_ = channel.Close()
}

var forwardID atomic.Int64

func (s *Server) portForward(target podTarget, port uint32) (io.ReadWriteCloser, error) {
	request := s.KubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(target.Namespace).
		Name(target.Name).
		SubResource("portforward")

	roundTripper, upgrader, err := spdy.RoundTripperFor(s.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("build portforward round tripper: %w", err)
	}
	connection, _, err := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, request.URL()).
		Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("portforward dial: %w", err)
	}

	headers := http.Header{}
	headers.Set(corev1.PortHeader, fmt.Sprint(port))
	headers.Set(corev1.PortForwardRequestIDHeader, fmt.Sprint(forwardID.Add(1)))
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	errorStream, err := connection.CreateStream(headers)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create portforward error stream: %w", err)
	}
	_ = errorStream.Close()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := connection.CreateStream(headers)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create portforward data stream: %w", err)
	}
	return &pfStream{ReadWriteCloser: dataStream, connection: connection}, nil
}

type pfStream struct {
	io.ReadWriteCloser
	connection io.Closer
}

func (s *pfStream) Close() error {
	_ = s.ReadWriteCloser.Close()
	return s.connection.Close()
}

func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	if bytes, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(bytes)
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 host key: %w", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}
	encoded := pem.EncodeToMemory(pemBlock)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write host key %s: %w", path, err)
	}
	return ssh.ParsePrivateKey(encoded)
}
