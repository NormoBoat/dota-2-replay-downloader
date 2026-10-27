package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type NewTask struct {
	Kind        string
	TargetID    *int64
	Priority    int
	RunAfter    *time.Time
	MaxAttempts int
	ParentID    *int64
	CreatedBy   string
}

// Enqueue adds a task unless an active (queued/running) one exists for the same
// kind and target. It returns the new id, or 0 and the active task's id.
func Enqueue(db *gorm.DB, t NewTask) (id int64, existing int64, err error) {
	if t.CreatedBy == "" {
		t.CreatedBy = "system"
	}
	if t.MaxAttempts == 0 {
		t.MaxAttempts = 5
	}
	runAfter := time.Now()
	if t.RunAfter != nil {
		runAfter = *t.RunAfter
	}
	var ids []int64
	err = db.Raw(`INSERT INTO tasks (kind, target_id, priority, run_after, max_attempts, parent_id, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, (coalesce(target_id, 0))) WHERE status IN ('queued','running') DO NOTHING
		RETURNING id`, t.Kind, t.TargetID, t.Priority, runAfter, t.MaxAttempts, t.ParentID, t.CreatedBy).Scan(&ids).Error
	if err != nil || len(ids) == 1 {
		if len(ids) == 1 {
			id = ids[0]
		}
		return id, 0, err
	}
	err = db.Raw(`SELECT id FROM tasks WHERE kind = ? AND coalesce(target_id, 0) = coalesce(?::bigint, 0)
		AND status IN ('queued','running') LIMIT 1`, t.Kind, t.TargetID).Scan(&ids).Error
	if len(ids) == 1 {
		existing = ids[0]
	}
	return 0, existing, err
}

func (s *Store) Enqueue(ctx context.Context, t NewTask) (int64, int64, error) {
	return Enqueue(s.DB.WithContext(ctx), t)
}

// Claim marks the next due task of one of kinds as running for this executor.
func (s *Store) Claim(ctx context.Context, kinds []string, accountID, proxyID *int64) (*Task, error) {
	var tasks []Task
	err := s.DB.WithContext(ctx).Raw(`UPDATE tasks SET status = 'running', attempts = attempts + 1,
			started_at = now(), updated_at = now(), account_id = ?, proxy_id = ?
		WHERE id = (SELECT id FROM tasks
			WHERE status = 'queued' AND kind IN ? AND run_after <= now()
			ORDER BY priority DESC, run_after, id
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING *`, accountID, proxyID, kinds).Scan(&tasks).Error
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	return &tasks[0], nil
}

// Finish ends a running task as done or skipped (reason kept in last_error).
func Finish(db *gorm.DB, id int64, status, reason string) error {
	return db.Exec(`UPDATE tasks SET status = ?, last_error = NULLIF(?, ''), finished_at = now(), updated_at = now()
		WHERE id = ? AND status = 'running'`, status, reason, id).Error
}

func (s *Store) Done(ctx context.Context, id int64) error {
	return Finish(s.DB.WithContext(ctx), id, StatusDone, "")
}

func (s *Store) Skip(ctx context.Context, id int64, reason string) error {
	return Finish(s.DB.WithContext(ctx), id, StatusSkipped, reason)
}

// Retry records a failed attempt: back to queue with exponential backoff
// (1 min * 2^(attempts-1), at most 1 h), or failed when attempts are used up.
func (s *Store) Retry(ctx context.Context, id int64, reason string) error {
	return s.DB.WithContext(ctx).Exec(`UPDATE tasks SET
			status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			run_after = now() + least(interval '1 minute' * power(2, greatest(attempts - 1, 0)), interval '1 hour'),
			finished_at = CASE WHEN attempts >= max_attempts THEN now() END,
			last_error = ?, updated_at = now()
		WHERE id = ? AND status = 'running'`, reason, id).Error
}

// Requeue returns a running task without spending the attempt: the executor,
// not the task, failed (logged out, proxy died, shutdown, live match not over).
func (s *Store) Requeue(ctx context.Context, id int64, after time.Duration, reason string) error {
	return s.DB.WithContext(ctx).Exec(`UPDATE tasks SET status = 'queued', attempts = greatest(attempts - 1, 0),
			run_after = now() + ? * interval '1 second', last_error = ?, updated_at = now()
		WHERE id = ? AND status = 'running'`, after.Seconds(), reason, id).Error
}

// RecoverStale requeues tasks left running longer than d (crash, kill -9).
func (s *Store) RecoverStale(ctx context.Context, d time.Duration) (int64, error) {
	res := s.DB.WithContext(ctx).Exec(`UPDATE tasks SET status = 'queued', attempts = greatest(attempts - 1, 0),
			last_error = 'recovered: was running at crash/restart', updated_at = now()
		WHERE status = 'running' AND started_at < now() - ? * interval '1 second'`, d.Seconds())
	return res.RowsAffected, res.Error
}

func (s *Store) CountTasks(ctx context.Context, kind, status string) (int64, error) {
	var n int64
	err := s.DB.WithContext(ctx).Model(&Task{}).Where("kind = ? AND status = ?", kind, status).Count(&n).Error
	return n, err
}

func (s *Store) GetTask(ctx context.Context, id int64) (*Task, error) {
	var t Task
	if err := s.DB.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}
