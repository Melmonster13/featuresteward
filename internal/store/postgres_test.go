//go:build integration

package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/auth/authtest"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/flag/flagtest"
	"github.com/Melmonster13/featuresteward/internal/idempotency"
	"github.com/Melmonster13/featuresteward/internal/idempotency/idemtest"
)

func TestPostgresFlagContract(t *testing.T) {
	newDB := dbFactory(t)
	flagtest.RunContract(t, func(t *testing.T) flag.Store { return newDB(t) })
}

func TestPostgresAuthContract(t *testing.T) {
	newDB := dbFactory(t)
	authtest.RunContract(t, func(t *testing.T) auth.Store { return newDB(t) })
}

func TestPostgresIdempotencyContract(t *testing.T) {
	newDB := dbFactory(t)
	idemtest.RunContract(t, func(t *testing.T, ttl, stale time.Duration) idempotency.Store {
		s := newDB(t)
		s.IdempotencyTTL, s.IdempotencyStale = ttl, stale
		return s
	})
}

// dbFactory returns a function giving each test its own database, cloned
// from a migrated template, since audit_events can't be truncated.
func dbFactory(t *testing.T) func(t *testing.T) *Postgres {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Fatal("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(ctx) })

	prefix := fmt.Sprintf("fs_test_%d", time.Now().UnixNano())
	template := prefix + "_template"
	createDB(t, admin, template, "")
	migrate(t, withDB(t, adminURL, template))

	var n atomic.Int64
	return func(t *testing.T) *Postgres {
		name := fmt.Sprintf("%s_%d", prefix, n.Add(1))
		createDB(t, admin, name, template)
		pool, err := pgxpool.New(ctx, withDB(t, adminURL, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return NewPostgres(pool)
	}
}

func createDB(t *testing.T, admin *pgx.Conn, name, template string) {
	t.Helper()
	ctx := context.Background()
	q := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		q += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	if _, err := admin.Exec(ctx, q); err != nil {
		t.Fatal(err)
	}
	// Registered on the test that created it, so pools closed in that
	// test's cleanup are already gone.
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
}

func migrate(t *testing.T, dbURL string) {
	t.Helper()
	sqlDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func withDB(t *testing.T, rawURL, name string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
