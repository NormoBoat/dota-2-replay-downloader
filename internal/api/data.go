package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/normo/dota-collector/internal/store"
	"gorm.io/gorm"
)

func (a *API) tasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var in struct {
			Kind     string `json:"kind"`
			Target   *int64 `json:"target_id"`
			Priority int    `json:"priority"`
		}
		if !decode(w, r, &in) {
			return
		}
		valid := false
		for _, k := range store.Kinds {
			if k == in.Kind {
				valid = true
			}
		}
		if !valid || in.Target == nil || *in.Target <= 0 {
			fail(w, 400, "bad_request", "invalid task kind or target")
			return
		}
		created, existing, err := a.Backend.Store.Enqueue(r.Context(), store.NewTask{Kind: in.Kind, TargetID: in.Target, Priority: in.Priority, CreatedBy: "api"})
		if dbError(w, err) {
			return
		}
		if existing > 0 {
			respond(w, 409, map[string]any{"error": map[string]any{"code": "conflict", "message": "task already active", "id": existing}})
			return
		}
		respond(w, 201, map[string]int64{"id": created})
		return
	}
	db := a.db(r)
	for _, k := range []string{"kind", "status", "target_id"} {
		if v := r.URL.Query().Get(k); v != "" {
			db = db.Where(k+" = ?", v)
		}
	}
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fail(w, 400, "bad_request", "invalid since")
			return
		}
		db = db.Where("created_at >= ?", t)
	}
	db, n, ok := page(w, r, db, "id")
	if !ok {
		return
	}
	var rows []store.Task
	if dbError(w, db.Find(&rows).Error) {
		return
	}
	listing(w, rows, n, func(t store.Task) int64 { return t.ID })
}
func (a *API) task(w http.ResponseWriter, r *http.Request) {
	i, ok := id(w, r)
	if !ok {
		return
	}
	row, err := a.Backend.Store.GetTask(r.Context(), i)
	if dbError(w, err) {
		return
	}
	if r.Method == "GET" {
		var children []store.Task
		var requests []store.GCRequest
		if dbError(w, a.db(r).Where("parent_id = ?", i).Find(&children).Error) {
			return
		}
		if dbError(w, a.db(r).Where("task_id = ?", i).Find(&requests).Error) {
			return
		}
		respond(w, 200, map[string]any{"task": row, "children": children, "requests": requests})
		return
	}
	var in struct {
		Priority *int       `json:"priority"`
		RunAfter *time.Time `json:"run_after"`
	}
	if !decode(w, r, &in) {
		return
	}
	changes := map[string]any{}
	if in.Priority != nil {
		changes["priority"] = *in.Priority
	}
	if in.RunAfter != nil {
		changes["run_after"] = *in.RunAfter
	}
	changes["updated_at"] = time.Now()
	result := a.db(r).Model(row).Where("status = 'queued'").Updates(changes)
	if dbError(w, result.Error) {
		return
	}
	if result.RowsAffected == 0 {
		fail(w, 409, "conflict", "only queued tasks can be edited")
		return
	}
	respond(w, 200, map[string]bool{"updated": true})
}
func (a *API) taskAction(w http.ResponseWriter, r *http.Request) {
	i, ok := id(w, r)
	if !ok {
		return
	}
	row, err := a.Backend.Store.GetTask(r.Context(), i)
	if dbError(w, err) {
		return
	}
	if strings.HasSuffix(r.URL.Path, "cancel") {
		res := a.db(r).Model(row).Where("status = 'queued'").Updates(map[string]any{"status": "cancelled", "finished_at": time.Now(), "updated_at": time.Now()})
		if dbError(w, res.Error) {
			return
		}
		if res.RowsAffected == 0 {
			fail(w, 409, "conflict", "only queued tasks can be cancelled")
			return
		}
		respond(w, 200, map[string]bool{"cancelled": true})
		return
	}
	if row.Status != "failed" && row.Status != "skipped" && row.Status != "cancelled" {
		fail(w, 409, "conflict", "task cannot be retried")
		return
	}
	created, existing, err := a.Backend.Store.Enqueue(r.Context(), store.NewTask{Kind: row.Kind, TargetID: row.TargetID, Priority: row.Priority, ParentID: &row.ID, CreatedBy: "api"})
	if dbError(w, err) {
		return
	}
	if existing > 0 {
		fail(w, 409, "conflict", "task already active")
		return
	}
	respond(w, 201, map[string]int64{"id": created})
}
func (a *API) taskSummary(w http.ResponseWriter, r *http.Request) {
	var rows []map[string]any
	if dbError(w, a.db(r).Raw(`SELECT kind,status,count(*) AS count,count(*) FILTER (WHERE status='queued' AND run_after<=now()) AS ready,min(created_at) AS oldest FROM tasks GROUP BY kind,status`).Scan(&rows).Error) {
		return
	}
	respond(w, 200, rows)
}
func (a *API) matches(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var in struct {
			IDs []int64 `json:"match_ids"`
		}
		if !decode(w, r, &in) {
			return
		}
		if len(in.IDs) > 500 {
			fail(w, 400, "bad_request", "at most 500 matches per request")
			return
		}
		for _, v := range in.IDs {
			if v <= 0 {
				fail(w, 400, "bad_request", "invalid match id")
				return
			}
		}
		err := a.db(r).Transaction(func(tx *gorm.DB) error {
			for _, i := range in.IDs {
				if err := store.Discover(tx, store.Match{MatchID: i, Source: "manual"}, nil, nil); err != nil {
					return err
				}
			}
			return nil
		})
		if dbError(w, err) {
			return
		}
		respond(w, 201, map[string]bool{"accepted": true})
		return
	}
	db := a.db(r).Omit("raw_details")
	for _, key := range []string{"has_details", "has_replay"} {
		if v := r.URL.Query().Get(key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				fail(w, 400, "bad_request", "invalid boolean filter")
				return
			}
			expr := "details_at IS NOT NULL"
			if key == "has_replay" {
				expr = "EXISTS (SELECT 1 FROM replays WHERE replays.match_id=matches.match_id)"
			}
			db = db.Where("("+expr+") = ?", b)
		}
	}
	if v := r.URL.Query().Get("game_mode"); v != "" {
		db = db.Where("game_mode = ?", v)
	}
	for _, key := range []string{"from", "to"} {
		if v := r.URL.Query().Get(key); v != "" {
			date, err := time.Parse(time.RFC3339, v)
			if err != nil {
				fail(w, 400, "bad_request", "invalid date filter")
				return
			}
			op := ">="
			if key == "to" {
				op = "<="
			}
			db = db.Where("start_time "+op+" ?", date)
		}
	}
	db, n, ok := page(w, r, db, "match_id")
	if !ok {
		return
	}
	var rows []store.Match
	if dbError(w, db.Find(&rows).Error) {
		return
	}
	listing(w, rows, n, func(m store.Match) int64 { return m.MatchID })
}
func (a *API) match(w http.ResponseWriter, r *http.Request) {
	i, ok := id(w, r)
	if !ok {
		return
	}
	var m store.Match
	var replay []store.ReplayMeta
	var tasks []store.Task
	db := a.db(r)
	if dbError(w, db.Omit("raw_details").First(&m, "match_id = ?", i).Error) {
		return
	}
	if dbError(w, db.Where("match_id = ?", i).Find(&replay).Error) {
		return
	}
	if dbError(w, db.Where("target_id = ? AND kind IN ?", i, []string{store.KindMatchDetails, store.KindReplayDownload}).Order("id DESC").Limit(50).Find(&tasks).Error) {
		return
	}
	respond(w, 200, map[string]any{"match": m, "replay": replay, "tasks": tasks})
}
func (a *API) replay(w http.ResponseWriter, r *http.Request) {
	i, ok := id(w, r)
	if !ok {
		return
	}
	var replay store.Replay
	if dbError(w, a.db(r).First(&replay, "match_id = ?", i).Error) {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d.dem.bz2"`, i))
	w.Header().Set("Content-Length", strconv.Itoa(len(replay.Data)))
	w.Write(replay.Data)
}
func (a *API) requests(w http.ResponseWriter, r *http.Request) {
	db := a.db(r)
	for _, key := range []string{"account_id", "kind", "result"} {
		if v := r.URL.Query().Get(key); v != "" {
			db = db.Where(key+" = ?", v)
		}
	}
	if v := r.URL.Query().Get("since"); v != "" {
		date, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fail(w, 400, "bad_request", "invalid since")
			return
		}
		db = db.Where("requested_at >= ?", date)
	}
	db, n, ok := page(w, r, db, "id")
	if !ok {
		return
	}
	var rows []store.GCRequest
	if dbError(w, db.Find(&rows).Error) {
		return
	}
	listing(w, rows, n, func(x store.GCRequest) int64 { return x.ID })
}
func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 3650 {
			fail(w, 400, "bad_request", "days must be 1..3650")
			return
		}
		days = n
	}
	var rows []map[string]any
	if dbError(w, a.db(r).Raw(`SELECT day, (SELECT count(*) FROM gc_requests WHERE requested_at>=day AND requested_at<day+interval '1 day') AS gc_requests,(SELECT count(*) FROM matches WHERE discovered_at>=day AND discovered_at<day+interval '1 day') AS matches,(SELECT count(*) FROM replays WHERE downloaded_at>=day AND downloaded_at<day+interval '1 day') AS replays,(SELECT coalesce(sum(size_bytes),0) FROM replays WHERE downloaded_at>=day AND downloaded_at<day+interval '1 day') AS bytes FROM generate_series(date_trunc('day',now())-(?-1)*interval '1 day',date_trunc('day',now()),interval '1 day') AS day`, days).Scan(&rows).Error) {
		return
	}
	respond(w, 200, rows)
}
