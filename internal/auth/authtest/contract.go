package authtest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/errs"
)

// RunContract checks behavior every auth.Store must share. newStore must
// return an empty store.
func RunContract(t *testing.T, newStore func(t *testing.T) auth.Store) {
	ctx := context.Background()

	t.Run("create and get user", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "mel", auth.RoleAdmin)
		if u.Handle != "mel" || u.Name != "Mel" || u.Role != auth.RoleAdmin || u.DisabledAt != nil || u.CreatedAt.IsZero() {
			t.Fatalf("created = %+v", u)
		}
		got, err := s.GetUser(ctx, "mel")
		if err != nil || got.Handle != "mel" || got.Role != auth.RoleAdmin {
			t.Fatalf("GetUser = %+v, %v", got, err)
		}
		if _, err := s.GetUser(ctx, "nope"); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("missing: got %v", err)
		}
	})

	t.Run("create user rejects duplicates and invalid input", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "mel", auth.RoleAdmin)
		if _, err := s.CreateUser(ctx, "system", "mel", "", auth.RoleViewer); !errors.Is(err, errs.ErrConflict) {
			t.Errorf("duplicate: got %v", err)
		}
		for _, h := range []string{"", "Mel", "-mel", "has space", strings.Repeat("a", 65)} {
			if _, err := s.CreateUser(ctx, "system", h, "", auth.RoleViewer); !errors.Is(err, errs.ErrInvalid) {
				t.Errorf("handle %q: got %v", h, err)
			}
		}
		if _, err := s.CreateUser(ctx, "system", "sam", "", "owner"); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("bad role: got %v", err)
		}
	})

	t.Run("list users sorted", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "zoe", auth.RoleViewer)
		mustUser(t, s, "ana", auth.RoleEditor)
		users, err := s.ListUsers(ctx)
		if err != nil || len(users) != 2 || users[0].Handle != "ana" || users[1].Handle != "zoe" {
			t.Fatalf("ListUsers = %+v, %v", users, err)
		}
	})

	t.Run("set role", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleViewer)
		u, err := s.SetRole(ctx, "mel", "sam", auth.RoleApprover)
		if err != nil || u.Role != auth.RoleApprover {
			t.Fatalf("SetRole = %+v, %v", u, err)
		}
		if _, err := s.SetRole(ctx, "mel", "sam", "root"); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("bad role: got %v", err)
		}
		if _, err := s.SetRole(ctx, "mel", "nope", auth.RoleAdmin); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("missing user: got %v", err)
		}
	})

	t.Run("token lifecycle", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleEditor)
		secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
		tok, err := s.CreateToken(ctx, "sam", "sam", "laptop", hash, prefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Name != "laptop" || tok.Prefix != prefix || !strings.HasPrefix(secret, prefix) || tok.RevokedAt != nil {
			t.Fatalf("token = %+v", tok)
		}

		u, err := s.Authenticate(ctx, auth.HashSecret(secret))
		if err != nil || u.Handle != "sam" || u.Role != auth.RoleEditor {
			t.Fatalf("Authenticate = %+v, %v", u, err)
		}
		if _, err := s.Authenticate(ctx, auth.HashSecret(secret+"x")); !errors.Is(err, errs.ErrUnauthorized) {
			t.Errorf("wrong secret: got %v", err)
		}

		toks, err := s.ListTokens(ctx, "sam")
		if err != nil || len(toks) != 1 || toks[0].ID != tok.ID || toks[0].LastUsedAt == nil {
			t.Fatalf("ListTokens = %+v, %v (want 1 token with last_used_at set)", toks, err)
		}

		if err := s.RevokeToken(ctx, "sam", "sam", tok.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(ctx, hash); !errors.Is(err, errs.ErrUnauthorized) {
			t.Errorf("revoked token: got %v", err)
		}
		if err := s.RevokeToken(ctx, "sam", "sam", tok.ID); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("revoke twice: got %v", err)
		}
	})

	t.Run("role changes apply to existing tokens", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleAdmin)
		secret := mustToken(t, s, "sam", nil)
		if _, err := s.SetRole(ctx, "mel", "sam", auth.RoleViewer); err != nil {
			t.Fatal(err)
		}
		if u, err := s.Authenticate(ctx, auth.HashSecret(secret)); err != nil || u.Role != auth.RoleViewer {
			t.Fatalf("Authenticate = %+v, %v; want viewer", u, err)
		}
	})

	t.Run("tokens are scoped to their owner", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleEditor)
		mustUser(t, s, "ana", auth.RoleEditor)
		_, hash, prefix := auth.NewSecret(auth.TokenPrefix)
		tok, err := s.CreateToken(ctx, "sam", "sam", "laptop", hash, prefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeToken(ctx, "ana", "ana", tok.ID); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("revoke someone else's token: got %v", err)
		}
		if toks, _ := s.ListTokens(ctx, "ana"); len(toks) != 0 {
			t.Errorf("ana sees %d tokens", len(toks))
		}
		if _, err := s.CreateToken(ctx, "ana", "ana", "copy", hash, prefix, nil); !errors.Is(err, errs.ErrConflict) {
			t.Errorf("duplicate hash: got %v", err)
		}
	})

	t.Run("token validation", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleEditor)
		_, hash, prefix := auth.NewSecret(auth.TokenPrefix)
		past := time.Now().Add(-time.Minute)
		if _, err := s.CreateToken(ctx, "sam", "sam", "old", hash, prefix, &past); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("past expiry: got %v", err)
		}
		if _, err := s.CreateToken(ctx, "sam", "sam", " ", hash, prefix, nil); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("blank name: got %v", err)
		}
		if _, err := s.CreateToken(ctx, "nope", "nope", "x", hash, prefix, nil); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("missing user: got %v", err)
		}
	})

	t.Run("expired tokens stop working", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleEditor)
		soon := time.Now().Add(500 * time.Millisecond)
		secret := mustToken(t, s, "sam", &soon)
		if _, err := s.Authenticate(ctx, auth.HashSecret(secret)); err != nil {
			t.Fatalf("before expiry: %v", err)
		}
		time.Sleep(time.Until(soon) + 200*time.Millisecond)
		if _, err := s.Authenticate(ctx, auth.HashSecret(secret)); !errors.Is(err, errs.ErrUnauthorized) {
			t.Errorf("after expiry: got %v", err)
		}
	})

	t.Run("disabling a user revokes access", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleAdmin)
		secret := mustToken(t, s, "sam", nil)
		if err := s.DisableUser(ctx, "mel", "sam"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(ctx, auth.HashSecret(secret)); !errors.Is(err, errs.ErrUnauthorized) {
			t.Errorf("disabled user: got %v", err)
		}
		toks, _ := s.ListTokens(ctx, "sam")
		if len(toks) != 1 || toks[0].RevokedAt == nil {
			t.Errorf("tokens not revoked: %+v", toks)
		}
		u, err := s.GetUser(ctx, "sam")
		if err != nil || u.DisabledAt == nil {
			t.Errorf("GetUser = %+v, %v; want disabled", u, err)
		}
		if _, err := s.SetRole(ctx, "mel", "sam", auth.RoleViewer); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("set role on disabled: got %v", err)
		}
		_, hash, prefix := auth.NewSecret(auth.TokenPrefix)
		if _, err := s.CreateToken(ctx, "mel", "sam", "new", hash, prefix, nil); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("token for disabled: got %v", err)
		}
		if err := s.DisableUser(ctx, "mel", "sam"); !errors.Is(err, errs.ErrNotFound) {
			t.Errorf("disable twice: got %v", err)
		}
		if _, err := s.CreateUser(ctx, "mel", "sam", "", auth.RoleViewer); !errors.Is(err, errs.ErrConflict) {
			t.Errorf("reuse disabled handle: got %v", err)
		}
	})

	t.Run("every mutation is audited without secrets", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "sam", auth.RoleViewer)
		if _, err := s.SetRole(ctx, "mel", "sam", auth.RoleEditor); err != nil {
			t.Fatal(err)
		}
		secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
		tok, err := s.CreateToken(ctx, "sam", "sam", "laptop", hash, prefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeToken(ctx, "sam", "sam", tok.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DisableUser(ctx, "mel", "sam"); err != nil {
			t.Fatal(err)
		}
		// Failed mutations must not be audited.
		s.SetRole(ctx, "mel", "sam", auth.RoleAdmin)

		events, err := s.ListUserAuditEvents(ctx, "sam")
		if err != nil {
			t.Fatal(err)
		}
		samViewer := map[string]any{"handle": "sam", "name": "Mel", "role": "viewer"}
		samEditor := map[string]any{"handle": "sam", "name": "Mel", "role": "editor"}
		tokMeta := map[string]any{"id": float64(tok.ID), "name": "laptop", "prefix": prefix}
		type ev struct {
			Actor, Action string
			Before, After any
		}
		want := []ev{
			{"system", auth.ActionUserCreated, nil, samViewer},
			{"mel", auth.ActionRoleChanged, samViewer, samEditor},
			{"sam", auth.ActionTokenCreated, nil, tokMeta},
			{"sam", auth.ActionTokenRevoked, tokMeta, nil},
			{"mel", auth.ActionUserDisabled, samEditor, nil},
		}
		if len(events) != len(want) {
			t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
		}
		for i, e := range events {
			got := ev{e.Actor, e.Action, decode(t, e.Before), decode(t, e.After)}
			if !reflect.DeepEqual(got, want[i]) {
				t.Errorf("event %d = %+v\nwant %+v", i, got, want[i])
			}
			if e.SubjectUser != "sam" || e.FlagKey != "" {
				t.Errorf("event %d: subject=%q flag=%q", i, e.SubjectUser, e.FlagKey)
			}
			for _, b := range [][]byte{e.Before, e.After} {
				if strings.Contains(string(b), secret) || strings.Contains(string(b), hex.EncodeToString(hash)) {
					t.Errorf("event %d leaks the token: %s", i, b)
				}
			}
		}
	})
}

func mustUser(t *testing.T, s auth.Store, handle string, role auth.Role) auth.User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), "system", handle, "Mel", role)
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", handle, err)
	}
	return u
}

func mustToken(t *testing.T, s auth.Store, handle string, expiresAt *time.Time) string {
	t.Helper()
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	if _, err := s.CreateToken(context.Background(), handle, handle, "test", hash, prefix, expiresAt); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return secret
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	if b == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("bad audit JSON %s: %v", b, err)
	}
	return v
}
