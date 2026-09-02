package ui

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"
)

// TODO: vendor xterm.js into embed.FS and replace textarea fallback frontend.
// Keep this websocket backend and gateway exec reuse path.

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
	},
}

type terminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type websocketWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
	kind string
}

func (w *websocketWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteJSON(terminalMessage{Type: w.kind, Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

type browserSizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func newBrowserSizeQueue() *browserSizeQueue {
	return &browserSizeQueue{ch: make(chan remotecommand.TerminalSize, 8)}
}

func (q *browserSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

func (q *browserSizeQueue) push(cols, rows uint16) {
	select {
	case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
	}
}

func (q *browserSizeQueue) close() {
	close(q.ch)
}

func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		_ = conn.WriteJSON(terminalMessage{Type: "error", Data: err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	defer stdinWriter.Close()
	sizes := newBrowserSizeQueue()
	defer sizes.close()

	stdout := &websocketWriter{conn: conn, kind: "output"}
	stderr := &websocketWriter{conn: conn, kind: "error"}
	errCh := make(chan error, 1)
	go func() {
		errCh <- api.OpenTerminal(ctx, TerminalStreamRequest{
			Namespace: session.Identity.UserSpaceNamespace,
			Name:      r.PathValue("name"),
			Term:      "xterm-256color",
			Stdin:     stdinReader,
			Stdout:    stdout,
			Stderr:    stderr,
			Sizes:     sizes,
		})
	}()

	sizes.push(120, 40)
	for {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			cancel()
			_ = stdinWriter.Close()
			break
		}
		switch message.Type {
		case "input":
			if _, err := stdinWriter.Write([]byte(message.Data)); err != nil {
				cancel()
				return
			}
		case "resize":
			sizes.push(message.Cols, message.Rows)
		}
		select {
		case err := <-errCh:
			if err != nil {
				_ = conn.WriteJSON(terminalMessage{Type: "error", Data: err.Error()})
			}
			return
		default:
		}
	}
	if err := <-errCh; err != nil {
		_ = conn.WriteJSON(terminalMessage{Type: "error", Data: err.Error()})
	}
}
