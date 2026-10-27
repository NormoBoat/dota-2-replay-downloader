package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/store"
	"github.com/normo/dota-collector/internal/worker"

	"gorm.io/gorm"
)

type API struct {
	Config  *atomic.Pointer[config.Config]
	Path    string
	Backend *Backend
	Restart chan struct{}
	State   *atomic.Value
	mu      sync.Mutex
}
type endpoint struct {
	pattern string
	fn      func(http.ResponseWriter, *http.Request)
}

func (a *API) endpoints() []endpoint {
	return []endpoint{
		{"GET /api/v1/health", a.health}, {"GET /api/v1/status", a.status},
		{"GET /api/v1/settings", a.settings}, {"PUT /api/v1/settings", a.settings}, {"POST /api/v1/restart", a.restart},
		{"GET /api/v1/account", a.account}, {"PATCH /api/v1/account", a.account},
		{"GET /api/v1/tasks", a.tasks}, {"POST /api/v1/tasks", a.tasks}, {"GET /api/v1/tasks/summary", a.taskSummary}, {"GET /api/v1/tasks/{id}", a.task}, {"PATCH /api/v1/tasks/{id}", a.task}, {"POST /api/v1/tasks/{id}/retry", a.taskAction}, {"POST /api/v1/tasks/{id}/cancel", a.taskAction},
		{"GET /api/v1/matches", a.matches}, {"POST /api/v1/matches", a.matches}, {"GET /api/v1/matches/{match_id}", a.match}, {"GET /api/v1/matches/{match_id}/replay", a.replay},
		{"GET /api/v1/gc-requests", a.requests}, {"GET /api/v1/stats/daily", a.stats},
	}
}
func Patterns() []string {
	var out []string
	for _, e := range (&API{}).endpoints() {
		out = append(out, e.pattern)
	}
	return out
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, e := range a.endpoints() {
		mux.HandleFunc(e.pattern, e.fn)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				slog.Error("HTTP handler panicked", "method", r.Method, "path", r.URL.Path)
				fail(w, 500, "internal", "request failed")
			}
		}()
		cfg := a.Config.Load()
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, v := range cfg.API.CORSOrigins {
				if v == origin {
					allowed = true
				}
			}
			if !allowed {
				fail(w, 403, "forbidden", "origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, OPTIONS")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/api/v1/health" && r.Method == "GET" {
			mux.ServeHTTP(w, r)
			return
		}
		if cfg.API.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+cfg.API.Token)) != 1 {
			fail(w, 401, "unauthorized", "bearer token required")
			return
		}
		if a.Backend == nil {
			fail(w, 503, "unavailable", "database is unavailable")
			return
		}
		if a.Backend.Stopping.Load() && r.Method != "GET" {
			fail(w, 503, "shutting_down", "collector is stopping")
			return
		}
		if _, pattern := mux.Handler(r); pattern == "" {
			for _, method := range []string{"GET", "POST", "PUT", "PATCH"} {
				check := r.Clone(r.Context())
				check.Method = method
				if _, p := mux.Handler(check); p != "" {
					fail(w, 405, "method_not_allowed", "method is not supported")
					return
				}
			}
			fail(w, 404, "not_found", "endpoint not found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := a.Backend.Store.Ping(ctx)
		cancel()
		if err != nil {
			fail(w, 503, "unavailable", "database is unavailable")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, kind, msg string) {
	respond(w, code, map[string]any{"error": map[string]string{"code": kind, "message": msg}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "bad_request", "invalid JSON or unknown field")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "bad_request", "expected one JSON object")
		return false
	}
	return true
}
func dbError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		fail(w, 409, "conflict", "record already exists")
	} else if errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23514" || strings.HasPrefix(pg.Code, "22")) {
		fail(w, 400, "bad_request", "invalid field or referenced record")
	} else if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, store.ErrNotFound) {
		fail(w, 404, "not_found", "record not found")
	} else {
		fail(w, 500, "internal", "database operation failed")
	}
	return true
}
func id(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		raw = r.PathValue("match_id")
	}
	if raw == "" {
		raw = r.PathValue("account_id")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		fail(w, 400, "bad_request", "id must be a positive integer")
		return 0, false
	}
	return v, true
}
func (a *API) db(r *http.Request) *gorm.DB { return a.Backend.Store.DB.WithContext(r.Context()) }
func page(w http.ResponseWriter, r *http.Request, db *gorm.DB, key string) (*gorm.DB, int, bool) {
	op, order := ">", " ASC"
	if r.URL.Path == "/api/v1/tasks" || r.URL.Path == "/api/v1/gc-requests" {
		op, order = "<", " DESC"
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			fail(w, 400, "bad_request", "limit must be 1..500")
			return nil, 0, false
		}
		limit = n
	}
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			fail(w, 400, "bad_request", "invalid cursor")
			return nil, 0, false
		}
		db = db.Where(key+" "+op+" ?", n)
	}
	return db.Order(key + order).Limit(limit + 1), limit, true
}
func listing[T any](w http.ResponseWriter, rows []T, limit int, key func(T) int64) {
	var next any
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatInt(key(rows[len(rows)-1]), 10)
	}
	if rows == nil {
		rows = []T{}
	}
	respond(w, 200, map[string]any{"items": rows, "next": next})
}
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ok := false
	if a.Backend != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		ok = a.Backend.Store.Ping(ctx) == nil
		cancel()
	}
	state := "starting"
	if a.State != nil {
		if s, yes := a.State.Load().(string); yes {
			state = s
		}
	}
	code := 200
	if !ok || state != "running" {
		code = 503
	}
	respond(w, code, map[string]any{"ok": ok && state == "running", "db": ok, "state": state})
}
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	db := a.db(r)
	var queues []map[string]any
	if dbError(w, db.Raw(`SELECT kind,status,count(*) AS count FROM tasks GROUP BY kind,status`).Scan(&queues).Error) {
		return
	}
	var accounts []map[string]any
	if dbError(w, db.Raw(`SELECT username,status,gc_remaining,gc_daily_limit,gc_reset_at,token_enc IS NOT NULL AS has_token,last_error,last_error_at FROM steam_accounts WHERE status <> 'disabled'`).Scan(&accounts).Error) {
		return
	}
	var size int64
	if dbError(w, db.Raw(`SELECT pg_database_size(current_database())`).Scan(&size).Error) {
		return
	}
	var account any
	if len(accounts) > 0 {
		account = accounts[0]
	}
	respond(w, 200, map[string]any{"tasks": queues, "account": account, "gc_online": worker.Online.Load(), "database_size_bytes": size})
}
func (a *API) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		respond(w, 200, a.Config.Load().Masked())
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var edit config.Config
	if !decode(w, r, &edit) {
		return
	}
	old := a.Config.Load()
	next := old.Merge(edit)
	if err := next.Validate(); err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	restart := old.NeedsRestart(next)
	if restart && a.Restart == nil {
		fail(w, 503, "unavailable", "process restart controller is unavailable")
		return
	}
	if err := config.Save(a.Path, &next); err != nil {
		fail(w, 500, "internal", "cannot save settings")
		return
	}
	a.Config.Store(&next)
	if restart {
		a.Backend.Stopping.Store(true)
	}
	respond(w, 200, map[string]bool{"restart": restart})
	if restart {
		select {
		case a.Restart <- struct{}{}:
		default:
		}
	}
}
func (a *API) restart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Restart == nil {
		fail(w, 503, "unavailable", "process restart controller is unavailable")
		return
	}
	a.Backend.Stopping.Store(true)
	respond(w, 202, map[string]bool{"restart": true})
	select {
	case a.Restart <- struct{}{}:
	default:
	}
}
