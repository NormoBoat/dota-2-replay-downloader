// Package store holds the PostgreSQL schema and every query the collector runs.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ DB *gorm.DB }

// Open connects without waiting for the server; callers retry Ping.
func Open(dsn string, maxConns int) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		// Parameters hold encrypted secrets and replay bytes: never log them.
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, err
	}
	db.Logger = logger.New(logWriter{}, logger.Config{SlowThreshold: time.Second, LogLevel: logger.Warn, ParameterizedQueries: true, IgnoreRecordNotFoundError: true})
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	return &Store{DB: db}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *Store) Close() error {
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Migrate applies migrations/NNNN_*.sql in order, each once, each in a transaction.
func (s *Store) Migrate(ctx context.Context) error {
	db := s.DB.WithContext(ctx)
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`).Error; err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, _ := migrations.ReadFile(name)
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(body)).Error; err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name).Error
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// GetState / SetState are the app_state key-value pair.
func (s *Store) GetState(ctx context.Context, key string) (string, bool, error) {
	var v []string
	err := s.DB.WithContext(ctx).Raw(`SELECT value FROM app_state WHERE key = ?`, key).Scan(&v).Error
	if err != nil || len(v) == 0 {
		return "", false, err
	}
	return v[0], true, nil
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	return s.DB.WithContext(ctx).Exec(`INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value).Error
}

var ErrNotFound = errors.New("not found")

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
