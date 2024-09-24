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
	err := clientproxy.DialAndServe(
		ctx,
		"https://localhost:4430/",
		"this_is_the_secret",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "hello from the other side")
		}),
	)
	if err != nil {
		panic(err)
	}
}
