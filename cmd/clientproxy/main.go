package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/daaku/clientproxy"
	"github.com/daaku/errgroup"
	"github.com/daaku/http2nc"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

type Proxy struct {
	Register string
	Secret   string
	Forward  string
}

type config struct {
	Proxy []Proxy
}

func serve(ctx context.Context, p Proxy) error {
	u, err := url.Parse(p.Forward)
	if err != nil {
		return errors.WithStack(err)
	}

	var h http.Handler
	switch u.Scheme {
	case "http", "https":
		h = httputil.NewSingleHostReverseProxy(u)
	case "tcp":
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := http2nc.DialConnect(w, r, u.Host); err != nil {
				log.Println(err)
			}
		})
	default:
		return errors.Errorf("unknown forward: %s", p.Forward)
	}

	return backoff.RetryNotify(
		func() error {
			if err := clientproxy.DialAndServe(ctx, p.Register, p.Secret, h); err != nil {
				// context errors are permanent, others are not
				if ctx.Err() == nil {
					return err
				} else {
					return backoff.Permanent(err)
				}
			}
			return nil
		},
		backoff.WithContext(
			backoff.NewExponentialBackOff(
				backoff.WithMaxElapsedTime(0),
			),
			ctx,
		),
		func(err error, _ time.Duration) {
			log.Printf("for %s: %v", p.Register, err)
		},
	)
}

func run(configPath string) error {
	f, err := os.Open(configPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var c config
	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(&c); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var eg errgroup.Group
	eg.Add(len(c.Proxy))
	for _, p := range c.Proxy {
		go func() {
			defer eg.Done()
			eg.Error(serve(ctx, p))
		}()
	}
	return eg.Wait()
}

func main() {
	configPath := "/etc/clientproxy.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	if err := run(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "%+v\n", err)
		os.Exit(1)
	}
}
