// Package app owns the collector lifetime. No runtime failure exits the process.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/normo/dota-collector/internal/api"
	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/secret"
	"github.com/normo/dota-collector/internal/store"
	"github.com/normo/dota-collector/internal/worker"
)

func Run(ctx context.Context, path string, cfg *config.Config) error {
	var current atomic.Pointer[config.Config]
	current.Store(cfg)
	for ctx.Err() == nil {
		if err := serve(ctx, path, &current); err != nil {
			slog.Error("collector stopped; restarting", "error", err)
		}
		if !worker.Wait(ctx, time.Second) {
			break
		}
	}
	slog.Info("stopped cleanly")
	return nil
}

// serve runs one generation: API, database, workers; it returns on stop, restart or failure.
func serve(ctx context.Context, path string, current *atomic.Pointer[config.Config]) error {
	cfg := current.Load()
	var state atomic.Value
	state.Store("starting")
	restart := make(chan struct{}, 1)
	var active atomic.Pointer[http.Handler]
	initial := (&api.API{Config: current, Path: path, State: &state, Restart: restart}).Handler()
	active.Store(&initial)
	server := &http.Server{Addr: cfg.API.Listen, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*active.Load()).ServeHTTP(w, r) })}
	listener, err := net.Listen("tcp", cfg.API.Listen)
	if err != nil {
		return errors.New("cannot listen on api.listen")
	}
	served := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				served <- errors.New("HTTP server panic")
			}
		}()
		served <- server.Serve(listener)
	}()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout.Duration)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			server.Close()
		}
	}()
	var s *store.Store
	for ctx.Err() == nil {
		s, err = store.Open(cfg.Database.DSN, cfg.Database.MaxOpenConns)
		if err == nil {
			mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = s.Migrate(mctx)
			cancel()
			if err == nil {
				break
			}
			s.Close()
			s = nil
		}
		slog.Error("PostgreSQL unavailable; retrying", "error", err)
		if !worker.Wait(ctx, 5*time.Second) {
			return nil
		}
	}
	if s == nil {
		return nil
	}
	defer s.Close()
	// One collector per database: a second one must not requeue the first one's running tasks.
	lock, err := pgx.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer lock.Close(context.Background())
	var locked bool
	if err = lock.QueryRow(ctx, "SELECT pg_try_advisory_lock(57002)").Scan(&locked); err != nil || !locked {
		return errors.New("another collector owns this database")
	}
	if _, err = s.RecoverStale(ctx, 0); err != nil {
		return err
	}
	key, err := cfg.SecretKey()
	if err != nil {
		return err
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	backend := &api.Backend{Store: s, Config: current}
	handler := (&api.API{Config: current, Path: path, State: &state, Restart: restart, Backend: backend}).Handler()
	active.Store(&handler)
	run, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recover() != nil {
				slog.Error("workers panic")
			}
		}()
		worker.Run(run, s, current, box)
	}()
	state.Store("running")
	select {
	case <-ctx.Done():
		state.Store("stopping")
	case <-restart:
		state.Store("restarting")
	case err = <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server stopped")
		}
	case <-done:
		slog.Error("workers stopped")
	}
	backend.Stopping.Store(true)
	// Workers requeue interrupted tasks themselves; wait for that within shutdown_timeout.
	cancel()
	select {
	case <-done:
	case <-time.After(cfg.App.ShutdownTimeout.Duration):
		return errors.New("shutdown timeout: workers did not stop")
	}
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_, err = s.RecoverStale(cleanup, 0)
	return err
}
