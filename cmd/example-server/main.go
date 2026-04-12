package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/daaku/clientproxy"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := clientproxy.Config{
		URL:    "https://localhost:4430/",
		Secret: "this_is_the_secret",
		Name:   "example",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "hello from the other side")
		}),
	}
	if err := clientproxy.DialAndServe(ctx, c); err != nil {
		panic(err)
	}
}
