package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/secret"
	"github.com/normo/dota-collector/internal/store"
)

// Run executes match_details (OpenDota, then GC) and replay_download tasks until
// ctx is cancelled, then waits for every goroutine. Interrupted tasks go back
// to the queue without spending an attempt.
func Run(ctx context.Context, s *store.Store, cfg *atomic.Pointer[config.Config], box *secret.Box) {
	var wg sync.WaitGroup
	loop := func(name string, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				func() {
					defer func() {
						if recover() != nil {
							slog.Error("component panic; restarting", "component", name)
						}
					}()
					fn()
				}()
				if !Wait(ctx, time.Second) {
					return
				}
			}
		}()
	}
	gc := &GC{Store: s, Box: box, Config: cfg}
	opendota := &http.Client{Timeout: time.Minute, Transport: &http.Transport{Proxy: nil}}
	loop("details", func() {
		defer gc.Close()
		for ctx.Err() == nil {
			gc.Idle()
			t, err := s.Claim(ctx, []string{store.KindMatchDetails}, nil, nil)
			if err != nil || t == nil {
				if !Wait(ctx, 5*time.Second) {
					return
				}
				continue
			}
			finish(ctx, s, *t, protect(func() error { return details(ctx, s, gc, *t, opendota) }))
			// OpenDota free tier: 60 requests per minute.
			if !Wait(ctx, time.Second) {
				return
			}
		}
	})
	for i := 0; i < 64; i++ {
		index := i
		loop("downloader", func() {
			for ctx.Err() == nil {
				c := cfg.Load().Downloader
				var t *store.Task
				var err error
				if index < c.Workers {
					t, err = s.Claim(ctx, []string{store.KindReplayDownload}, nil, nil)
				}
				if err != nil || t == nil {
					if !Wait(ctx, 5*time.Second) {
						return
					}
					continue
				}
				client := &http.Client{Timeout: c.Timeout.Duration, Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}}
				dctx, cancel := context.WithTimeout(ctx, c.Timeout.Duration)
				err = protect(func() error { return Download(dctx, s, *t, client) })
				cancel()
				client.CloseIdleConnections()
				finish(ctx, s, *t, err)
			}
		})
	}
	wg.Wait()
}

// details: OpenDota first; on any OpenDota failure the GC of the one account, within its limit.
func details(ctx context.Context, s *store.Store, gc *GC, t store.Task, client *http.Client) error {
	if t.TargetID == nil {
		return errors.New("missing match id")
	}
	err := ResolveTask(ctx, s, t, client)
	if err == nil || ctx.Err() != nil {
		return err
	}
	gcErr := gc.Details(ctx, t)
	if errors.Is(gcErr, errNoGC) {
		return fmt.Errorf("%v; %v", err, gcErr)
	}
	return gcErr
}

func protect(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("task executor panicked; attempt will be retried")
		}
	}()
	return f()
}

// finish records a failed attempt; nil means the executor already finished the task.
func finish(ctx context.Context, s *store.Store, t store.Task, err error) {
	if err == nil {
		return
	}
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	var wait *waitError
	var save error
	switch {
	case ctx.Err() != nil:
		save = s.Requeue(cleanup, t.ID, 0, "interrupted by shutdown")
	case errors.As(err, &wait):
		slog.Warn("task postponed", "task", t.ID, "kind", t.Kind, "until", wait.until, "reason", wait.reason)
		save = s.Requeue(cleanup, t.ID, max(time.Until(wait.until), time.Minute), wait.reason)
	default:
		slog.Error("task failed", "task", t.ID, "kind", t.Kind, "error", err)
		save = s.Retry(cleanup, t.ID, err.Error())
	}
	if save != nil {
		slog.Error("cannot persist task outcome", "task", t.ID, "error", save)
	}
}

func Wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
