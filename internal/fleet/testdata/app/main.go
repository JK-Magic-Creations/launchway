// A tiny service for the fleet tests: the release is the binary, and the
// build says what it is (-X main.version=... -X main.mode=...).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version, mode = "v0", "good" // mode: good, bad500 (healthy but fails requests), badhealth

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if mode == "badhealth" {
			http.Error(w, "not ready", 500)
			return
		}
		fmt.Fprintln(w, "ok", version)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		if mode == "bad500" {
			http.Error(w, "broken "+version, 500)
			return
		}
		w.Header().Set("X-Version", version)
		fmt.Fprintln(w, "hello from", version)
	})
	srv := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
		<-c
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		srv.Shutdown(ctx) // finish what is in flight
	}()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
