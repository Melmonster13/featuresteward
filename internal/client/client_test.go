package client_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/client"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/httpapi"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
)

// TestRetryAfterLostResponse drops the connection after the server has
// created the flag. The client retries with the same Idempotency-Key, so
// the flag is created once and the retry gets the original response.
func TestRetryAfterLostResponse(t *testing.T) {
	ctx := context.Background()
	flags, users := flagtest.NewMemory(), authtest.NewMemory()
	api := httpapi.NewRouter(flags, users, idemtest.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	var mu sync.Mutex
	var keys []string
	dropped := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			if r.Header.Get("Idempotency-Key") != "" {
				t.Error("GET sent an Idempotency-Key")
			}
			api.ServeHTTP(w, r)
			return
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if !dropped {
			dropped = true
			api.ServeHTTP(httptest.NewRecorder(), r) // applied, but the response is lost
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer srv.Close()

	users.CreateUser(ctx, "system", "mel", "", auth.RoleAdmin)
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	users.CreateToken(ctx, "system", "mel", "test", hash, prefix, nil)

	c := client.New(srv.URL, secret)
	f, err := c.CreateFlag(ctx, "dark-mode", "Dark mode", "", "")
	if err != nil {
		t.Fatalf("create after a lost response: %v", err)
	}
	if f.Key != "dark-mode" {
		t.Errorf("flag = %+v", f)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("Idempotency-Keys = %q; want the same key twice", keys)
	}
	events, _ := flags.ListAuditEvents(ctx, "dark-mode")
	if len(events) != 1 {
		t.Errorf("got %d audit events, want 1 (created once)", len(events))
	}
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}

	// Separate calls get separate keys.
	c.CreateFlag(ctx, "other", "Other", "", "")
	if len(keys) != 3 || keys[2] == keys[0] {
		t.Errorf("second call reused a key: %q", keys)
	}
}
