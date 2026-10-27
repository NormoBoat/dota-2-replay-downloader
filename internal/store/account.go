package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SaveLogin stores the token of the one GC account and disables any other row.
func (s *Store) SaveLogin(ctx context.Context, username string, tokenEnc []byte, limit int) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Account{}).Where("username <> ? AND status <> 'disabled'", username).Update("status", "disabled").Error; err != nil {
			return err
		}
		now := time.Now()
		a := Account{Username: username, TokenEnc: tokenEnc, Status: "active", GCDailyLimit: limit, GCRemaining: limit, GCResetAt: now.Add(24 * time.Hour), LastLoginAt: &now}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "username"}},
			DoUpdates: clause.Assignments(map[string]any{"token_enc": tokenEnc, "status": "active", "guard_type": nil,
				"cooldown_until": nil, "last_login_at": now, "last_error": nil, "last_error_at": nil})}).Create(&a).Error
	})
}

// GCAccount returns the active account with a token, or nil when GC is not configured.
func (s *Store) GCAccount(ctx context.Context) (*Account, error) {
	var rows []Account
	err := s.DB.WithContext(ctx).Where("status = 'active' AND token_enc IS NOT NULL").Order("id").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// AccountError records a Steam/GC problem; status "" keeps the current status.
func (s *Store) AccountError(ctx context.Context, id int64, status, msg string) error {
	v := map[string]any{"last_error": msg, "last_error_at": time.Now()}
	if status != "" {
		v["status"] = status
	}
	return s.DB.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(v).Error
}

// ReserveGC spends one GC request of the 24 h window (restarted on first use after
// gc_reset_at) and journals it as pending; nil means the quota is exhausted.
func (s *Store) ReserveGC(ctx context.Context, accountID int64, t Task) (*GCRequest, error) {
	var req *GCRequest
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE steam_accounts SET gc_remaining = gc_daily_limit, gc_reset_at = now() + interval '24 hours'
			WHERE id = ? AND gc_reset_at <= now()`, accountID).Error; err != nil {
			return err
		}
		r := tx.Exec(`UPDATE steam_accounts SET gc_remaining = gc_remaining - 1 WHERE id = ? AND status = 'active' AND gc_remaining > 0`, accountID)
		if r.Error != nil || r.RowsAffected == 0 {
			return r.Error
		}
		req = &GCRequest{AccountID: accountID, TaskID: &t.ID, Kind: t.Kind, TargetID: t.TargetID, Result: "pending"}
		if t.ID == 0 {
			req.TaskID = nil
		}
		return tx.Create(req).Error
	})
	return req, err
}
