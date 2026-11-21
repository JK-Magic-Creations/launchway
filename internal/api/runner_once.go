package api

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Runner writes are safe to repeat (see engine/idempotent.go). Here: the
// Idempotency-Key header, and the pieces of the log and finish calls.

// maxRememberedAnswer is the largest first answer kept for a repeat.
const maxRememberedAnswer = 64 << 10

// once makes a runner write safe to repeat. A call that carries an
// Idempotency-Key is answered, if the same caller already made it
// successfully, with the first answer, and the work is not done again;
// otherwise it runs, and a successful answer is kept. A call without the
// header (an older runner) runs as it always did.
func (s *Server) once(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 200 {
			h(w, r)
			return
		}
		key = nonEmpty(callerID(r), "shared") + "|" + r.Method + " " + r.URL.Path + "|" + key
		if status, body, ok := s.Engine.RecallWrite(key); ok {
			replay(w, status, body)
			return
		}
		rec := &answerRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status/100 == 2 && rec.body.Len() <= maxRememberedAnswer {
			if err := s.Engine.RememberWrite(key, rec.status, rec.body.Bytes()); err != nil {
				s.Log.Warn("could not record the answer to a runner write", "err", err)
			}
		}
	}
}

func replay(w http.ResponseWriter, status int, body []byte) {
	if len(body) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// answerRecorder passes an answer on and keeps a copy of it.
type answerRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *answerRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *answerRecorder) Write(b []byte) (int, error) {
	if r.body.Len() <= maxRememberedAnswer {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}
func (r *answerRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// appendLogs stores a batch of log lines. A runner that counts its lines
// sends the offset of the batch; lines the server has are skipped, and a
// batch that starts beyond what the server has is answered 409 with the
// count it has, so that the runner sends from there.
func (s *Server) appendLogs(w http.ResponseWriter, in leaseReq) {
	offset := -1
	if in.Offset != nil {
		offset = max(*in.Offset, 0)
	}
	err := s.Engine.AppendLogsAt(in.LeaseID, offset, in.Lines)
	var gap *engine.LogGapError
	if errors.As(err, &gap) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "received": gap.Have})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// finishHandled answers, and reports true, if a finish needs no more work:
// the attempt was already finished with this lease (a repeat), or the lease
// is not the caller's.
func (s *Server) finishHandled(w http.ResponseWriter, r *http.Request, leaseID string) bool {
	if s.Engine.FinishRecorded(leaseID) {
		replay(w, http.StatusNoContent, nil)
		return true
	}
	return !s.leaseAllowed(w, r, leaseID)
}
