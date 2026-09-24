package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Melmonster13/featuresteward/internal/idempotency"
	"github.com/Melmonster13/featuresteward/internal/store/db"
)

var _ idempotency.Store = (*Postgres)(nil)

func (s *Postgres) Begin(ctx context.Context, user, key string, requestHash []byte) (*idempotency.Record, error) {
	// A concurrent Release can delete the row between the two queries, so retry.
	for range 3 {
		_, err := s.q.BeginIdempotencyKey(ctx, db.BeginIdempotencyKeyParams{
			UserHandle: user, Key: key, RequestHash: requestHash,
			TtlSecs: s.IdempotencyTTL.Seconds(), StaleSecs: s.IdempotencyStale.Seconds(),
		})
		if err == nil {
			return nil, nil // reserved
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		row, err := s.q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{UserHandle: user, Key: key})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		rec := &idempotency.Record{RequestHash: row.RequestHash, Body: row.Body}
		if row.Status != nil {
			rec.Status = int(*row.Status)
		}
		if row.Headers != nil {
			if err := json.Unmarshal(row.Headers, &rec.Headers); err != nil {
				return nil, err
			}
		}
		return rec, nil
	}
	return nil, errors.New("idempotency key contended")
}

func (s *Postgres) Complete(ctx context.Context, user, key string, status int, headers map[string]string, body []byte) error {
	h, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	st := int32(status)
	return s.q.CompleteIdempotencyKey(ctx, db.CompleteIdempotencyKeyParams{
		UserHandle: user, Key: key, Status: &st, Headers: h, Body: body,
	})
}

func (s *Postgres) Release(ctx context.Context, user, key string) error {
	return s.q.ReleaseIdempotencyKey(ctx, db.ReleaseIdempotencyKeyParams{UserHandle: user, Key: key})
}

func (s *Postgres) DeleteExpired(ctx context.Context) error {
	return s.q.DeleteExpiredIdempotencyKeys(ctx, s.IdempotencyTTL.Seconds())
}
