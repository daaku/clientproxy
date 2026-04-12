// Package clientproxy provides a method to dial into a Caddy server and use
// this process to serve HTTP requests.
package clientproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	urlp "net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

var yamuxConfig = &yamux.Config{
	AcceptBacklog:          256,
	EnableKeepAlive:        true,
	KeepAliveInterval:      5 * time.Minute,
	ConnectionWriteTimeout: 10 * time.Second,
	MaxStreamWindowSize:    256 * 1024,
	StreamCloseTimeout:     5 * time.Minute,
	StreamOpenTimeout:      75 * time.Second,
	LogOutput:              os.Stderr,
}

// Config to
type Config struct {
	URL     string
	Secret  string
	Name    string
	Handler http.Handler
}

// DialAndServe dials and serves the given config. The
// URL must contain the scheme, and the secret must be as set to match the server.
func DialAndServe(ctx context.Context, c Config) error {
	if c.URL == "" || c.Secret == "" || c.Name == "" || c.Handler == nil {
		return fmt.Errorf("clientproxy: incomplete configuration")
	}
	u, err := urlp.Parse(c.URL)
	if err != nil {
		return err
	}
	var conn net.Conn
	addr := u.Host
	if u.Scheme == "https" {
		if u.Port() == "" {
			addr += ":443"
		}
		dialer := tls.Dialer{Config: &tls.Config{ServerName: u.Hostname()}}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	} else {
		if u.Port() == "" {
			addr += ":80"
		}
		dialer := net.Dialer{}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("clientproxy: DialAndServe: %w", err)
	}
	defer conn.Close() // defensive close, ServeConn will handle this for us
	b := strings.Join([]string{
		"GET ", u.RequestURI(), " HTTP/1.1\r\n",
		"Host: ", u.Hostname(), "\r\n",
		"X-Client-Proxy-Secret: ", c.Secret, "\r\n",
		"X-Client-Proxy-Name: ", c.Name, "\r\n",
		"\r\n",
	}, "")
	if _, err := conn.Write([]byte(b)); err != nil {
		return err
	}
	yamuxServer, err := yamux.Server(conn, yamuxConfig)
	if err != nil {
		return fmt.Errorf("clientproxy: DialAndServe: %w", err)
	}
	// close the connection if the context is canceled. this will release the
	// http.Server and we'll return from the outer function.
	context.AfterFunc(ctx, func() {
		yamuxServer.Close()
	})
	if err := http.Serve(yamuxServer, c.Handler); err != nil {
		// if the contextErr is not set, we failed for an unknown reason.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("clientproxy: DialAndServe: %w", err)
	}
	return errors.New("clientproxy: DialAndServe: unknown error")
}
