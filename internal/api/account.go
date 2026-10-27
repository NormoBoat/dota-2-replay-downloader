package api

import (
	"net/http"

	"github.com/normo/dota-collector/internal/store"
	"github.com/normo/dota-collector/internal/worker"
)

// account shows and edits the one GC account; logging in is `collector login` (needs a terminal for Guard).
func (a *API) account(w http.ResponseWriter, r *http.Request) {
	var rows []store.Account
	if dbError(w, a.db(r).Order("id").Find(&rows).Error) {
		return
	}
	// The non-disabled account (login disables the others), else the first one.
	var row *store.Account
	for i := range rows {
		if row == nil || rows[i].Status != "disabled" {
			row = &rows[i]
		}
	}
	if r.Method == "GET" {
		var requests []store.GCRequest
		if dbError(w, a.db(r).Order("id DESC").Limit(50).Find(&requests).Error) {
			return
		}
		var view any
		if row != nil {
			view = struct {
				store.Account
				HasToken bool `json:"has_token"`
			}{*row, len(row.TokenEnc) > 0}
		}
		respond(w, 200, map[string]any{"account": view, "online": worker.Online.Load(), "requests": requests})
		return
	}
	if row == nil {
		fail(w, 404, "not_found", "no account: run collector login")
		return
	}
	var in struct {
		Status *string `json:"status"`
		Limit  *int    `json:"gc_daily_limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	changes := map[string]any{}
	if in.Status != nil {
		if *in.Status != "active" && *in.Status != "disabled" {
			fail(w, 400, "bad_request", "status must be active or disabled")
			return
		}
		changes["status"] = *in.Status
	}
	if in.Limit != nil {
		if *in.Limit < 1 {
			fail(w, 400, "bad_request", "gc_daily_limit must be positive")
			return
		}
		changes["gc_daily_limit"] = *in.Limit
	}
	if len(changes) > 0 && dbError(w, a.db(r).Model(row).Updates(changes).Error) {
		return
	}
	respond(w, 200, map[string]bool{"updated": true})
}
