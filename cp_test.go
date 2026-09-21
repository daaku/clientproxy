package clientproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

const secret = "s3cret"

// quietYamuxLog silences the shared yamux config for the duration of a test
// and returns a copy of it for the yamux sessions the test creates itself.
func quietYamuxLog(t *testing.T) *yamux.Config {
	t.Helper()
	peer := *yamuxConfig
	peer.LogOutput = io.Discard
	before := yamuxConfig.LogOutput
	yamuxConfig.LogOutput = io.Discard
	t.Cleanup(func() { yamuxConfig.LogOutput = before })
	return &peer
}

// deadConn reads like a connection whose peer stopped answering, which the
// kernel reports as a timeout, and which net.Error therefore calls temporary.
type deadConn struct{}

func (deadConn) Read([]byte) (int, error) {
	return 0, &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: &os.SyscallError{Syscall: "read", Err: os.ErrDeadlineExceeded},
	}
}

func (deadConn) Write(p []byte) (int, error)      { return len(p), nil }
func (deadConn) Close() error                     { return nil }
func (deadConn) LocalAddr() net.Addr              { return deadAddr{} }
func (deadConn) RemoteAddr() net.Addr             { return deadAddr{} }
func (deadConn) SetDeadline(time.Time) error      { return nil }
func (deadConn) SetReadDeadline(time.Time) error  { return nil }
func (deadConn) SetWriteDeadline(time.Time) error { return nil }

type deadAddr struct{}

func (deadAddr) Network() string { return "tcp" }
func (deadAddr) String() string  { return "10.0.7.8:35858->13.201.42.47:443" }

// A yamux session that cannot accept is over. The error it reports for a
// stalled connection looks like a timeout to net/http, which then retried the
// accept forever ("http: Accept error: ...; retrying in 1s") instead of
// returning, so the caller never reconnected.
func TestServeEndsTemporaryAcceptErrors(t *testing.T) {
	quietYamuxLog(t)
	errs := make(chan error, 1)
	go func() {
		errs <- serve(context.Background(), deadConn{}, Config{
			Name:    "test",
			Handler: http.NotFoundHandler(),
		})
	}()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("serve returned no error")
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("serve returned %v, want it to wrap %v", err, os.ErrDeadlineExceeded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return, the http.Server is retrying a fatal error")
	}
}

func TestDialAndServeServesUntilThePeerGoesAway(t *testing.T) {
	session, errs, _ := startClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from the client")
	}))
	if code, body := get(t, session, "/hello"); code != 200 || body != "hello from the client" {
		t.Fatalf("got %d %q, want 200 %q", code, body, "hello from the client")
	}
	session.Close()
	select {
	case err := <-errs:
		if err == nil {
			t.Error("DialAndServe returned no error after the peer went away")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DialAndServe did not return after the peer went away")
	}
}

func TestDialAndServeStopsOnContextCancel(t *testing.T) {
	session, errs, cancel := startClient(t, http.NotFoundHandler())
	if code, _ := get(t, session, "/hello"); code != 404 {
		t.Fatalf("got %d, want 404", code)
	}
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("DialAndServe returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DialAndServe did not return after cancel")
	}
}

// startClient dials the caddy side of a connection, started by startPeer, and
// serves the given handler with it. It returns the caddy side session, the
// errors of DialAndServe and its cancel function.
func startClient(t *testing.T, h http.Handler) (*yamux.Session, chan error, context.CancelFunc) {
	t.Helper()
	peerCfg := quietYamuxLog(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	peers := make(chan *yamux.Session, 1)
	go startPeer(ln, peers, peerCfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errs := make(chan error, 1)
	go func() {
		errs <- DialAndServe(ctx, Config{
			URL:     "http://" + ln.Addr().String(),
			Secret:  secret,
			Name:    "test",
			Handler: h,
		})
	}()
	session := <-peers
	if session == nil {
		t.Fatal("could not set up the peer side")
	}
	t.Cleanup(func() { session.Close() })
	return session, errs, cancel
}

// startPeer behaves like the caddy module: it reads the registration request
// and then turns the same connection around into a yamux client. It reports
// the session, or nil if it could not be created.
func startPeer(ln net.Listener, peers chan<- *yamux.Session, cfg *yamux.Config) {
	conn, err := ln.Accept()
	if err != nil {
		peers <- nil
		return
	}
	r := bufio.NewReader(conn)
	if _, err := http.ReadRequest(r); err != nil {
		conn.Close()
		peers <- nil
		return
	}
	session, err := yamux.Client(&bufConn{Conn: conn, Reader: r}, cfg)
	if err != nil {
		conn.Close()
		peers <- nil
		return
	}
	peers <- session
}

// bufConn replays what reading the registration request already buffered
// before it reads from the connection itself again.
type bufConn struct {
	net.Conn
	Reader *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	if c.Reader == nil || c.Reader.Buffered() == 0 {
		c.Reader = nil
		return c.Conn.Read(p)
	}
	return c.Reader.Read(p)
}

// get issues an HTTP GET over a stream of the session and returns the
// response code and body.
func get(t *testing.T, session *yamux.Session, path string) (int, string) {
	t.Helper()
	stream, err := session.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()
	req, err := http.NewRequest("GET", "http://daaku.org"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(stream); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}
