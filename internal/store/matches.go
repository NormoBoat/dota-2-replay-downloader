package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func Discover(tx *gorm.DB, m Match, parent *int64, after *time.Time) error {
	r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&m)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return nil
	}
	_, _, err := Enqueue(tx, NewTask{Kind: KindMatchDetails, TargetID: &m.MatchID, ParentID: parent, RunAfter: after})
	return err
}
