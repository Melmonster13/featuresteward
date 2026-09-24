package idemtest

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/idempotency"
)

// RunContract checks behavior every idempotency.Store must share.
// newStore must return an empty store using the given TTL and stale timeout.
func RunContract(t *testing.T, newStore func(t *testing.T, ttl, stale time.Duration) idempotency.Store) {
	ctx := context.Background()
	hash := []byte("hash-1")

	t.Run("reserve, complete, replay", func(t *testing.T) {
		s := newStore(t, time.Hour, time.Minute)
		if rec, err := s.Begin(ctx, "mel", "k1", hash); err != nil || rec != nil {
			t.Fatalf("first Begin = %+v, %v; want reserved", rec, err)
		}
		rec, err := s.Begin(ctx, "mel", "k1", hash)
		if err != nil || rec == nil || rec.Status != 0 || !bytes.Equal(rec.RequestHash, hash) {
			t.Fatalf("Begin while in progress = %+v, %v", rec, err)
		}
		headers := map[string]string{"Location": "/api/v1/flags/x"}
		if err := s.Complete(ctx, "mel", "k1", 201, headers, []byte(`{"key":"x"}`)); err != nil {
			t.Fatal(err)
		}
		rec, err = s.Begin(ctx, "mel", "k1", []byte("different"))
		want := &idempotency.Record{RequestHash: hash, Status: 201, Headers: headers, Body: []byte(`{"key":"x"}`)}
		if err != nil || !reflect.DeepEqual(rec, want) {
			t.Fatalf("replay = %+v, %v; want %+v", rec, err, want)
		}
	})

	t.Run("keys are per user", func(t *testing.T) {
		s := newStore(t, time.Hour, time.Minute)
		s.Begin(ctx, "mel", "k1", hash)
		if rec, err := s.Begin(ctx, "sam", "k1", hash); err != nil || rec != nil {
			t.Fatalf("other user's Begin = %+v, %v; want reserved", rec, err)
		}
	})

	t.Run("release frees an in-progress key only", func(t *testing.T) {
		s := newStore(t, time.Hour, time.Minute)
		s.Begin(ctx, "mel", "k1", hash)
		if err := s.Release(ctx, "mel", "k1"); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.Begin(ctx, "mel", "k1", hash); rec != nil {
			t.Fatalf("after release = %+v; want reserved", rec)
		}
		s.Complete(ctx, "mel", "k1", 200, nil, nil)
		s.Release(ctx, "mel", "k1")
		if rec, _ := s.Begin(ctx, "mel", "k1", hash); rec == nil || rec.Status != 200 {
			t.Fatalf("release removed a completed record: %+v", rec)
		}
	})

	t.Run("abandoned requests can be retried", func(t *testing.T) {
		s := newStore(t, time.Hour, 300*time.Millisecond)
		s.Begin(ctx, "mel", "k1", hash)
		time.Sleep(500 * time.Millisecond)
		if rec, err := s.Begin(ctx, "mel", "k1", []byte("hash-2")); err != nil || rec != nil {
			t.Fatalf("stale Begin = %+v, %v; want reserved", rec, err)
		}
		s.Complete(ctx, "mel", "k1", 201, nil, nil)
		time.Sleep(500 * time.Millisecond)
		// Completed records outlive the stale timeout.
		if rec, _ := s.Begin(ctx, "mel", "k1", hash); rec == nil || !bytes.Equal(rec.RequestHash, []byte("hash-2")) {
			t.Fatalf("completed record lost: %+v", rec)
		}
	})

	t.Run("records expire", func(t *testing.T) {
		s := newStore(t, 300*time.Millisecond, time.Minute)
		s.Begin(ctx, "mel", "old", hash)
		s.Complete(ctx, "mel", "old", 201, nil, nil)
		time.Sleep(500 * time.Millisecond)
		if rec, err := s.Begin(ctx, "mel", "old", hash); err != nil || rec != nil {
			t.Fatalf("expired Begin = %+v, %v; want reserved", rec, err)
		}
		s.Begin(ctx, "mel", "gone", hash)
		time.Sleep(500 * time.Millisecond)
		if err := s.DeleteExpired(ctx); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.Begin(ctx, "mel", "gone", hash); rec != nil {
			t.Fatalf("DeleteExpired left %+v", rec)
		}
	})
}
