package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/normo/dota-collector/internal/store"
	"gorm.io/gorm"
)

// OpenDotaURL is the public OpenDota API; tests point it at httptest.
var OpenDotaURL = "https://api.opendota.com/api"

// ErrNoMatch means OpenDota does not know the match: retrying will not help.
var ErrNoMatch = errors.New("match not found on OpenDota")

type openDotaMatch struct {
	MatchID    int64  `json:"match_id"`
	Cluster    *int   `json:"cluster"`
	ReplaySalt *int64 `json:"replay_salt"`
	StartTime  *int64 `json:"start_time"`
	Duration   *int   `json:"duration"`
	GameMode   *int   `json:"game_mode"`
	LobbyType  *int   `json:"lobby_type"`
	RadiantWin *bool  `json:"radiant_win"`
	RadiantSc  *int   `json:"radiant_score"`
	DireScore  *int   `json:"dire_score"`
}

// Resolve reads replay coordinates and match metadata from OpenDota.
func Resolve(ctx context.Context, client *http.Client, matchID int64) (*openDotaMatch, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/matches/%d", OpenDotaURL, matchID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Join(errors.New("OpenDota network error"), ctx.Err())
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, ErrNoMatch
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OpenDota HTTP %d", resp.StatusCode)
	}
	var m openDotaMatch
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&m); err != nil {
		return nil, errors.New("OpenDota returned invalid JSON")
	}
	if m.MatchID != matchID {
		return nil, ErrNoMatch
	}
	return &m, nil
}

// ResolveTask runs a match_details task: store coordinates and queue the download.
func ResolveTask(ctx context.Context, s *store.Store, t store.Task, client *http.Client) error {
	if t.TargetID == nil {
		return errors.New("missing match id")
	}
	m, err := Resolve(ctx, client, *t.TargetID)
	if err != nil {
		return err
	}
	if m.Cluster == nil || *m.Cluster <= 0 || m.ReplaySalt == nil || *m.ReplaySalt <= 0 {
		return errNoCoordinates
	}
	now := time.Now()
	row := map[string]any{"cluster": *m.Cluster, "replay_salt": *m.ReplaySalt, "details_at": now,
		"duration_s": m.Duration, "game_mode": m.GameMode, "lobby_type": m.LobbyType,
		"radiant_win": m.RadiantWin, "radiant_score": m.RadiantSc, "dire_score": m.DireScore}
	if m.StartTime != nil {
		row["start_time"] = time.Unix(*m.StartTime, 0).UTC()
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.Match{}).Where("match_id = ?", m.MatchID).Updates(row).Error; err != nil {
			return err
		}
		if _, _, err := store.Enqueue(tx, store.NewTask{Kind: store.KindReplayDownload, TargetID: t.TargetID, ParentID: &t.ID}); err != nil {
			return err
		}
		return store.Finish(tx, t.ID, store.StatusDone, "")
	})
}

// errNoCoordinates: OpenDota has the match but not parsed yet; retry with backoff.
var errNoCoordinates = errors.New("OpenDota has no cluster/replay_salt for this match yet")
