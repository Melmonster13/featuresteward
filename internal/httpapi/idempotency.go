package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	idempotencyHeader = "Idempotency-Key"
	replayedHeader    = "Idempotent-Replayed"
)

// storedHeaders are the response headers kept for replays.
var storedHeaders = []string{"Content-Type", "Location", "Cache-Control"}

// idempotent makes a state-changing route safe to retry. When a request
// carries an Idempotency-Key, the first response for that user and key
// is stored and replayed for identical retries within a day.
//
// With redactSecrets, the "token" and "key" fields are dropped before
// storing, so secrets never reach the database; a replay returns the
// created object's metadata without its secret.
func (s *server) idempotent(redactSecrets bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(idempotencyHeader)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !validIdempotencyKey(key) {
			writeError(w, http.StatusBadRequest, "Idempotency-Key must be 1-255 printable ASCII characters")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			} else {
				writeError(w, http.StatusBadRequest, "could not read request body")
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		h := sha256.New()
		io.WriteString(h, r.Method+" "+r.URL.RequestURI()+"\n")
		h.Write(body)
		reqHash := h.Sum(nil)

		ctx := context.WithoutCancel(r.Context())
		user := actor(r)
		rec, err := s.idem.Begin(ctx, user, key, reqHash)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if rec != nil {
			switch {
			case !bytes.Equal(rec.RequestHash, reqHash):
				writeError(w, http.StatusUnprocessableEntity, "this Idempotency-Key was already used for a different request")
			case rec.Status == 0:
				writeError(w, http.StatusConflict, "a request with this Idempotency-Key is still in progress")
			default:
				for k, v := range rec.Headers {
					w.Header().Set(k, v)
				}
				w.Header().Set(replayedHeader, "true")
				w.WriteHeader(rec.Status)
				w.Write(rec.Body)
			}
			return
		}

		rw := &recorder{ResponseWriter: w}
		completed := false
		defer func() {
			// Server errors (and panics) leave nothing applied, so free
			// the key for a retry.
			if !completed {
				if err := s.idem.Release(ctx, user, key); err != nil {
					s.log.ErrorContext(ctx, "release idempotency key", "err", err)
				}
			}
		}()
		next.ServeHTTP(rw, r)
		if rw.status >= 500 {
			return
		}
		stored := rw.body.Bytes()
		if redactSecrets {
			stored = redact(stored)
		}
		headers := map[string]string{}
		for _, k := range storedHeaders {
			if v := w.Header().Get(k); v != "" {
				headers[k] = v
			}
		}
		completed = true
		if err := s.idem.Complete(ctx, user, key, rw.status, headers, stored); err != nil {
			// The change was applied; keeping the key in progress blocks
			// retries until it goes stale rather than risking a repeat.
			s.log.ErrorContext(ctx, "complete idempotency key", "err", err)
		}
	})
}

func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > 255 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

func redact(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	delete(m, "token")
	delete(m, "key")
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

// recorder passes a response through while keeping a copy.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
