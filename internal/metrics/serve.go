package metrics

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// ContentType is the media type of the text exposition format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves the registry at /metrics. A non-empty token is required as
// "Authorization: Bearer <token>"; the comparison takes the same time whatever
// the guess. Nothing else is served: every other path answers 404, so a
// request meant for the product's own address finds nothing here.
func Handler(r *Registry, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, req *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		_ = r.Write(w)
	})
	return mux
}

// Serve listens on addr and serves the registry until ctx ends. It returns
// the bound address (useful when addr names port 0) as soon as it listens.
func Serve(ctx context.Context, r *Registry, addr, token string) (string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: Handler(r, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err // a failed metrics listener never stops the product
		}
	}()
	return ln.Addr().String(), nil
}
