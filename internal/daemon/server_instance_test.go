package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// newInstanceTestServer builds a minimal Server bound to a short temp socket
// (Unix socket paths have a tight length limit, so t.TempDir() overflows it).
func newInstanceTestServer(t *testing.T, exitWithClients bool) *Server {
	t.Helper()
	sock := filepath.Join("/tmp", fmt.Sprintf("vixd-inst-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(sock) })
	srv := NewServer(sock, config.Credential{}, "test-session", "test-model", &config.DaemonConfig{}, PluginConfig{})
	srv.SetExitWithClients(exitWithClients)
	return srv
}

// serve starts the server in a goroutine and waits until it is accepting
// connections. It returns the done channel (carrying ListenAndServe's result)
// and the cancel func.
func serve(t *testing.T, srv *Server) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	// Wait until the socket accepts connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", srv.sockPath)
		if err == nil {
			c.Close()
			return done, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("server did not start listening in time")
	return done, cancel
}

// registerInstance dials the server and sends an instance.register command,
// returning the open connection. Closing it signals the daemon the instance
// detached.
func registerInstance(t *testing.T, sock string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, _ := json.Marshal(protocol.InstanceRegisterData{Mode: "tui"})
	cmd := protocol.SessionCommand{Type: "instance.register", Data: data}
	payload, _ := json.Marshal(cmd)
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		conn.Close()
		t.Fatalf("write register: %v", err)
	}
	return conn
}

// waitInstanceCount polls until the server's instance count equals want, or
// fails after a timeout.
func waitInstanceCount(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.instanceMu.Lock()
		n := srv.instanceCount
		srv.instanceMu.Unlock()
		if n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.instanceMu.Lock()
	n := srv.instanceCount
	srv.instanceMu.Unlock()
	t.Fatalf("instance count = %d, want %d", n, want)
}

// TestInstanceExitWithClients: the daemon shuts down after its last instance
// disconnects when exit-with-clients is enabled.
func TestInstanceExitWithClients(t *testing.T) {
	old := exitGracePeriod
	exitGracePeriod = 150 * time.Millisecond
	defer func() { exitGracePeriod = old }()

	srv := newInstanceTestServer(t, true)
	done, cancel := serve(t, srv)
	defer cancel()

	conn := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 1)

	conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not shut down after last instance disconnected")
	}
}

// TestInstanceNoExitWhenDisabled: without the flag, the daemon stays up after
// instances disconnect.
func TestInstanceNoExitWhenDisabled(t *testing.T) {
	old := exitGracePeriod
	exitGracePeriod = 150 * time.Millisecond
	defer func() { exitGracePeriod = old }()

	srv := newInstanceTestServer(t, false)
	done, cancel := serve(t, srv)
	defer cancel()

	conn := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 1)
	conn.Close()
	waitInstanceCount(t, srv, 0)

	select {
	case <-done:
		t.Fatal("daemon shut down despite exit-with-clients being disabled")
	case <-time.After(500 * time.Millisecond):
		// Still running, as expected.
	}

	cancel()
	<-done
}

// TestInstanceNoExitBeforeAnyConnect: a daemon with the flag set but no instance
// ever attached does not shut down (everHadInstance guard).
func TestInstanceNoExitBeforeAnyConnect(t *testing.T) {
	old := exitGracePeriod
	exitGracePeriod = 150 * time.Millisecond
	defer func() { exitGracePeriod = old }()

	srv := newInstanceTestServer(t, true)
	done, cancel := serve(t, srv)
	defer cancel()

	select {
	case <-done:
		t.Fatal("daemon shut down before any instance attached")
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	<-done
}

// TestInstanceMultiple: shutdown happens only after the last of several
// instances disconnects.
func TestInstanceMultiple(t *testing.T) {
	old := exitGracePeriod
	exitGracePeriod = 150 * time.Millisecond
	defer func() { exitGracePeriod = old }()

	srv := newInstanceTestServer(t, true)
	done, cancel := serve(t, srv)
	defer cancel()

	c1 := registerInstance(t, srv.sockPath)
	c2 := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 2)

	// Close one — daemon must stay up.
	c1.Close()
	waitInstanceCount(t, srv, 1)
	select {
	case <-done:
		t.Fatal("daemon shut down while an instance was still attached")
	case <-time.After(400 * time.Millisecond):
	}

	// Close the last — daemon shuts down.
	c2.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not shut down after the last instance disconnected")
	}
}

// TestInstanceRelaunchWithinGrace: a new instance connecting within the grace
// window cancels the pending shutdown.
func TestInstanceRelaunchWithinGrace(t *testing.T) {
	old := exitGracePeriod
	exitGracePeriod = 400 * time.Millisecond
	defer func() { exitGracePeriod = old }()

	srv := newInstanceTestServer(t, true)
	done, cancel := serve(t, srv)
	defer cancel()

	c1 := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 1)
	c1.Close()
	waitInstanceCount(t, srv, 0)

	// Reconnect well within the grace window.
	time.Sleep(100 * time.Millisecond)
	c2 := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 1)

	// The original grace timer should have been cancelled; daemon stays up past
	// the original deadline.
	select {
	case <-done:
		t.Fatal("daemon shut down despite a reconnect within the grace window")
	case <-time.After(600 * time.Millisecond):
	}

	c2.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not shut down after the reconnected instance left")
	}
}
