package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/cache"
	"github.com/Melmonster13/featuresteward/internal/flag"
	"github.com/Melmonster13/featuresteward/internal/httpapi"
	"github.com/Melmonster13/featuresteward/internal/ratelimit"
	"github.com/Melmonster13/featuresteward/internal/store"
	"github.com/Melmonster13/featuresteward/internal/usage"
	"github.com/Melmonster13/featuresteward/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var err error
	if len(os.Args) > 1 && os.Args[1] == "create-admin" {
		err = createAdmin(os.Args[2:])
	} else {
		err = run(log)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func connect(ctx context.Context) (*pgxpool.Pool, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func run(log *slog.Logger) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := connectRedis(ctx, os.Getenv("REDIS_URL"), log)
	if err != nil {
		return err
	}
	var redisCheck func(context.Context) error
	if rdb != nil {
		defer rdb.Close()
		redisCheck = func(ctx context.Context) error { return rdb.Ping(ctx).Err() }
	}

	db := store.NewPostgres(pool)
	ttl, err := secondsEnv("CACHE_TTL_SECONDS", 30)
	if err != nil {
		return err
	}
	var flags flag.Store = db
	var users auth.Store = db
	switch {
	case rdb != nil && ttl > 0:
		flags = cache.New(db, rdb, ttl, log)
		users = cache.NewAuth(db, rdb, ttl, log)
	case rdb != nil:
		log.Info("CACHE_TTL_SECONDS is 0; evaluations aren't cached")
	}
	opts := []httpapi.Option{httpapi.WithHealthCheck("redis", redisCheck)}
	perMin, err := intEnv("RATE_LIMIT_PER_MIN", 600, "requests")
	if err != nil {
		return err
	}
	switch {
	case rdb != nil && perMin > 0:
		opts = append(opts, httpapi.WithRateLimiter(ratelimit.New(rdb, perMin, log)))
	case rdb != nil:
		log.Info("RATE_LIMIT_PER_MIN is 0; evaluations aren't rate limited")
	}
	staleDays, err := intEnv("STALE_AFTER_DAYS", 30, "days")
	if err != nil {
		return err
	}
	if staleDays == 0 {
		return errors.New("STALE_AFTER_DAYS must be at least 1")
	}
	opts = append(opts, httpapi.WithStaleAfter(time.Duration(staleDays)*24*time.Hour))
	staleDigest, err := newDigest(db, time.Duration(staleDays)*24*time.Hour, log)
	if err != nil {
		return err
	}
	recorder := usage.New(db, log)
	opts = append(opts, httpapi.WithUsage(recorder.Seen))
	api := httpapi.NewRouter(flags, users, db, log, opts...)
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/healthz", api)
	mux.Handle("/", web.Handler())
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	runDigest := func() {
		if staleDigest == nil {
			return
		}
		if err := staleDigest.Run(ctx); err != nil {
			log.Error("send the stale flag digest", "err", err)
		}
	}
	go func() {
		runDigest()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := db.DeleteExpired(ctx); err != nil {
					log.Error("delete expired idempotency keys", "err", err)
				}
				if err := db.DeleteExpiredSessions(ctx); err != nil {
					log.Error("delete expired sessions", "err", err)
				}
				if n, err := db.ExpireChangeRequests(ctx); err != nil {
					log.Error("expire change requests", "err", err)
				} else if n > 0 {
					log.Info("expired change requests", "count", n)
				}
				runDigest()
			}
		}
	}()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	recorded := make(chan struct{})
	go func() {
		recorder.Run(ctx, time.Minute)
		close(recorded)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	<-recorded // the final flush of flag usage
	return err
}
