package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/daaku/clientproxy"
	"github.com/daaku/errgroup"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

type Proxy struct {
	Register string
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
	rp := httputil.NewSingleHostReverseProxy(u)
	return backoff.RetryNotify(
		func() error {
			if err := clientproxy.DialAndServe(ctx, p.Register, rp); err != nil {
				// context errors are permanent, others are not
				if ctx.Err() == nil {
					return err
				} else {
					return backoff.Permanent(err)
				}
			}
			return nil
		},
		backoff.NewExponentialBackOff(
			backoff.WithMaxElapsedTime(0),
		),
		func(err error, _ time.Duration) {
			log.Println(err)
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
