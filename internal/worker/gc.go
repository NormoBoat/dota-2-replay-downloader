package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/secret"
	"github.com/normo/dota-collector/internal/steamx"
	"github.com/normo/dota-collector/internal/store"
	"github.com/paralin/go-dota2/protocol"
	"gorm.io/gorm"
)

// gcIdle closes the session soon after the last request, so the owner can play.
const gcIdle = 2 * time.Minute

// errNoGC: no active account with a token (run `collector login`); OpenDota stays the only source.
var errNoGC = errors.New("GC account not configured or not active (run collector login)")

// waitError: GC is temporarily unusable (quota, backoff, owner playing); requeue without spending an attempt.
type waitError struct {
	until  time.Time
	reason string
}

func (w *waitError) Error() string { return w.reason }

// GC requests match details from the one account's GC, opening the session on demand.
// It is used by a single goroutine.
type GC struct {
	Store    *store.Store
	Box      *secret.Box
	Config   *atomic.Pointer[config.Config]
	session  *steamx.Session
	lastUsed time.Time
	retryAt  time.Time
	failures int
}

// Online reports whether a GC session is open (read by the API).
var Online atomic.Bool

func (g *GC) Close() {
	if g.session != nil {
		g.session.Close()
		g.session = nil
	}
	Online.Store(false)
}

// Idle closes a dead session or one unused for gcIdle.
func (g *GC) Idle() {
	if g.session != nil && (!g.session.Alive() || time.Since(g.lastUsed) > gcIdle) {
		g.Close()
	}
}

func (g *GC) open(ctx context.Context, a *store.Account) error {
	if g.session != nil && g.session.Alive() {
		return nil
	}
	g.Close()
	if time.Now().Before(g.retryAt) {
		return &waitError{g.retryAt, "GC session backoff after an error"}
	}
	token, err := g.Box.Open(a.TokenEnc)
	if err != nil {
		return err
	}
	session, err := steamx.Open(ctx, a.Username, token, g.Config.Load().GC)
	if err == nil {
		g.session, g.failures, g.lastUsed = session, 0, time.Now()
		Online.Store(true)
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	slog.Error("Steam GC session failed", "account", a.Username, "error", err)
	var login *steamx.LoginError
	switch {
	case errors.Is(err, steamx.ErrPlayingElsewhere):
		g.retryAt = time.Now().Add(15 * time.Minute)
	case errors.As(err, &login) && login.Status == "throttled":
		g.retryAt = time.Now().Add(time.Hour)
	case errors.As(err, &login):
		// The token is rejected: never retry it in a loop, wait for `collector login`.
		if save := g.Store.AccountError(ctx, a.ID, login.Status, err.Error()+"; run collector login"); save != nil {
			return save
		}
		return fmt.Errorf("%w: %v", errNoGC, err)
	default:
		g.failures++
		g.retryAt = time.Now().Add(min(time.Minute<<min(g.failures-1, 6), time.Hour))
	}
	if save := g.Store.AccountError(ctx, a.ID, "", err.Error()); save != nil {
		slog.Error("cannot save account error", "account", a.Username)
	}
	return &waitError{g.retryAt, "GC: " + err.Error()}
}

// Details resolves a match_details task through the GC: errNoGC or *waitError when GC cannot be used.
func (g *GC) Details(ctx context.Context, t store.Task) error {
	a, err := g.Store.GCAccount(ctx)
	if err != nil {
		return err
	}
	if a == nil {
		return errNoGC
	}
	if a.GCRemaining <= 0 && a.GCResetAt.After(time.Now()) {
		return &waitError{a.GCResetAt, "GC daily limit exhausted"}
	}
	if err := g.open(ctx, a); err != nil {
		return err
	}
	request, err := g.Store.ReserveGC(ctx, a.ID, t)
	if err != nil {
		return err
	}
	if request == nil {
		return &waitError{time.Now().Add(time.Minute), "GC daily limit exhausted"}
	}
	start := time.Now()
	rctx, stop := context.WithTimeout(ctx, g.Config.Load().GC.RequestTimeout.Duration)
	resp, err := g.session.Dota.RequestMatchDetails(rctx, uint64(*t.TargetID))
	stop()
	g.lastUsed = time.Now()
	result := "ok"
	if err != nil {
		result = "error"
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// GC is silent past its limit: stop until the window resets rather than probe it.
			result = "timeout"
			if save := g.Store.DB.WithContext(ctx).Model(&store.Account{}).Where("id = ?", a.ID).Update("gc_remaining", 0).Error; save != nil {
				slog.Error("cannot persist GC quota timeout", "account", a.Username)
			}
		}
		slog.Error("GC match details failed", "account", a.Username, "match_id", *t.TargetID, "error", err)
		if save := g.Store.AccountError(ctx, a.ID, "", "GC match details: "+err.Error()); save != nil {
			slog.Error("cannot save account error", "account", a.Username)
		}
	}
	journal := map[string]any{"result": result, "latency_ms": time.Since(start).Milliseconds()}
	if resp != nil {
		journal["eresult"] = int(resp.GetResult())
	}
	if save := g.Store.DB.WithContext(ctx).Model(request).Updates(journal).Error; save != nil {
		slog.Error("GC journal update failed", "account", a.Username)
	}
	if err != nil {
		return fmt.Errorf("GC match details: %w", err)
	}
	return SaveGCDetails(ctx, g.Store, t, resp)
}

// SaveGCDetails stores coordinates and metadata from the GC and queues the download.
func SaveGCDetails(ctx context.Context, s *store.Store, t store.Task, resp *protocol.CMsgGCMatchDetailsResponse) error {
	if resp.GetResult() != 1 {
		return fmt.Errorf("GC match details result %d", resp.GetResult())
	}
	m := resp.GetMatch()
	if m == nil || int64(m.GetMatchId()) != *t.TargetID {
		return errors.New("GC returned wrong match")
	}
	if m.GetDuration() == 0 {
		return &waitError{time.Now().Add(15 * time.Minute), "match is still live"}
	}
	raw, err := m.MarshalVT()
	if err != nil {
		return err
	}
	state := m.GetReplayState()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values := map[string]any{"details_at": time.Now(), "cluster": int(m.GetCluster()), "replay_salt": int64(m.GetReplaySalt()),
			"replay_state": state.String(), "duration_s": int(m.GetDuration()), "start_time": time.Unix(int64(m.GetStarttime()), 0).UTC(),
			"game_mode": int(m.GetGameMode()), "lobby_type": int(m.GetLobbyType()), "radiant_score": int(m.GetRadiantTeamScore()),
			"dire_score": int(m.GetDireTeamScore()), "raw_details": raw}
		if err := tx.Model(&store.Match{}).Where("match_id = ?", *t.TargetID).Updates(values).Error; err != nil {
			return err
		}
		if state != protocol.CMsgDOTAMatch_REPLAY_AVAILABLE || m.GetCluster() == 0 || m.GetReplaySalt() == 0 {
			return store.Finish(tx, t.ID, store.StatusSkipped, "replay not available: "+state.String())
		}
		if _, _, err := store.Enqueue(tx, store.NewTask{Kind: store.KindReplayDownload, TargetID: t.TargetID, ParentID: &t.ID}); err != nil {
			return err
		}
		return store.Finish(tx, t.ID, store.StatusDone, "")
	})
}
