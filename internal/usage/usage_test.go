package usage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Melmonster13/featuresteward/internal/flag"
)

type fakeStore struct {
	mu      sync.Mutex
	batches [][]flag.Evaluation
	fail    bool
}

func (f *fakeStore) RecordEvaluations(_ context.Context, seen []flag.Evaluation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("database down")
	}
	f.batches = append(f.batches, seen)
	return nil
}

func (f *fakeStore) latest(flagKey, env string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t time.Time
	for _, b := range f.batches {
		for _, e := range b {
			if e.Flag == flagKey && e.Environment == env && e.At.After(t) {
				t = e.At
			}
		}
	}
	return t
}

func newRecorder() (*Recorder, *fakeStore, *time.Time) {
	store := &fakeStore{}
	r := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	return r, store, &now
}

func TestFlushWritesTheLatestPerFlagAndEnvironment(t *testing.T) {
	r, store, now := newRecorder()
	ctx := context.Background()
	r.Seen("a", "prod")
	*now = now.Add(time.Second)
	r.Seen("a", "prod")
	r.Seen("a", "dev")
	r.Seen("b", "prod")
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.batches) != 1 || len(store.batches[0]) != 3 {
		t.Fatalf("batches = %v", store.batches)
	}
	if got := store.latest("a", "prod"); !got.Equal(*now) {
		t.Errorf("a/prod = %v, want %v", got, *now)
	}
	// Nothing new: nothing written.
	r.Flush(ctx)
	if len(store.batches) != 1 {
		t.Errorf("empty flush wrote %d batches", len(store.batches))
	}
}

func TestFailedFlushesAreRetried(t *testing.T) {
	r, store, now := newRecorder()
	ctx := context.Background()
	r.Seen("a", "prod")
	first := *now
	store.fail = true
	if err := r.Flush(ctx); err == nil {
		t.Fatal("no error")
	}
	*now = now.Add(time.Minute)
	r.Seen("b", "prod")
	store.fail = false
	r.Flush(ctx)
	if got := store.latest("a", "prod"); !got.Equal(first) {
		t.Errorf("a/prod after retry = %v, want %v", got, first)
	}
	if got := store.latest("b", "prod"); !got.Equal(*now) {
		t.Errorf("b/prod = %v", got)
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	r, store, _ := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, time.Hour); close(done) }()
	r.Seen("a", "prod")
	cancel()
	<-done
	if store.latest("a", "prod").IsZero() {
		t.Error("shutdown didn't flush")
	}
}

func TestSeenIsSafeUnderLoad(t *testing.T) {
	r, store, _ := newRecorder()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				r.Seen("a", []string{"dev", "prod"}[i%2])
			}
		}()
	}
	for range 5 {
		r.Flush(context.Background())
	}
	wg.Wait()
	r.Flush(context.Background())
	if store.latest("a", "dev").IsZero() || store.latest("a", "prod").IsZero() {
		t.Error("lost evaluations")
	}
}
