// Package usage remembers when flags are evaluated and writes it to the
// store periodically, so evaluating a flag never writes to the database.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Melmonster13/featuresteward/internal/flag"
)

// Store is the part of flag.Store the recorder writes to.
type Store interface {
	RecordEvaluations(ctx context.Context, seen []flag.Evaluation) error
}

type key struct{ flag, env string }

type Recorder struct {
	store Store
	log   *slog.Logger
	now   func() time.Time

	mu   sync.Mutex
	seen map[key]time.Time
}

func New(store Store, log *slog.Logger) *Recorder {
	return &Recorder{store: store, log: log, now: time.Now, seen: map[key]time.Time{}}
}

// Seen notes that a flag was evaluated in env just now.
func (r *Recorder) Seen(flagKey, env string) {
	t := r.now()
	r.mu.Lock()
	r.seen[key{flagKey, env}] = t
	r.mu.Unlock()
}

// Flush writes what's been seen since the last flush. If the write fails,
// the times are kept for the next one.
func (r *Recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	seen := r.seen
	r.seen = map[key]time.Time{}
	r.mu.Unlock()
	if len(seen) == 0 {
		return nil
	}
	batch := make([]flag.Evaluation, 0, len(seen))
	for k, t := range seen {
		batch = append(batch, flag.Evaluation{Flag: k.flag, Environment: k.env, At: t})
	}
	err := r.store.RecordEvaluations(ctx, batch)
	if err != nil {
		r.mu.Lock()
		for k, t := range seen {
			if t.After(r.seen[k]) {
				r.seen[k] = t
			}
		}
		r.mu.Unlock()
	}
	return err
}

// Run flushes every interval until ctx ends, then flushes once more.
func (r *Recorder) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				r.log.Error("record flag evaluations", "err", err)
			}
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Flush(final); err != nil {
				r.log.Error("record flag evaluations", "err", err)
			}
			return
		}
	}
}
