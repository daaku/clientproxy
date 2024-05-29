// Package clientproxy provides a method to dial into a Caddy server and use
// this process to serve HTTP requests.
package clientproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	urlp "net/url"
	"sync/atomic"

	"golang.org/x/net/http2"
)

// DialAndServe connects to the given URL and serves the provided handler. The
// URL must contain the scheme, and the path must be correctly set to the
// secret expected by the server.
func DialAndServe(ctx context.Context, url string, h http.Handler) error {
	ctx, cancel := context.WithCancel(ctx)

	// this will ensure our background goroutine below will be released if our
	// connection fails for reasons besides a context cancelation.
	defer cancel()

	u, err := urlp.Parse(url)
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
	var b bytes.Buffer
	b.WriteString("GET ")
	b.WriteString(u.RequestURI())
	b.WriteString(" HTTP/1.1\r\nHost: ")
	b.WriteString(u.Hostname())
	b.WriteString("\r\n\r\n")
	if _, err := conn.Write(b.Bytes()); err != nil {
		return err
	}
	var lastErrType atomic.Value
	h2s := &http2.Server{
		CountError: func(errType string) {
			lastErrType.Store(errType)
		},
	}
	// close the connection if the context is canceled. this will release the
	// ServeConn and we'll return from the outer function.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	h2s.ServeConn(conn, &http2.ServeConnOpts{
		Context: ctx,
		Handler: h,
	})
	if errType, ok := lastErrType.Load().(string); ok {
		return fmt.Errorf("clientproxy: DialAndServe: ServeConn failed with %s", errType)
	}
	// if the contextErr is not set, we failed for an unknown reason.
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("clientproxy: DialAndServe: unknown error")
}
