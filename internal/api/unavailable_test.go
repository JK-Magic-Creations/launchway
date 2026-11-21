package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A request that outlives the database's outage answers 503 with
// Retry-After and a plain message, never a 500.
func TestUnavailableDatabaseAnswers503WithRetryAfter(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for name, err := range map[string]error{
		"budget spent":     &engine.UnavailableError{Err: fmt.Errorf("dial tcp: connection refused"), Waited: 2 * time.Minute},
		"wrapped":          fmt.Errorf("creating the run: %w", &engine.UnavailableError{Err: io.ErrUnexpectedEOF, Ambiguous: true}),
		"raw transient":    fmt.Errorf("read tcp: %w", io.ErrUnexpectedEOF),
		"engine is closed": engine.ErrClosed,
	} {
		w := httptest.NewRecorder()
		s.fail(w, err)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", name, w.Code)
		}
		if w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After", name)
		}
		if strings.Contains(w.Body.String(), "dial tcp") || strings.Contains(w.Body.String(), "EOF") {
			t.Errorf("%s: the message leaks internals: %s", name, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.fail(w, fmt.Errorf("a plain bug"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("a permanent error is still a 500, got %d", w.Code)
	}
}
