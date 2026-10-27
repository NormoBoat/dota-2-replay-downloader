package store

import (
	"fmt"
	"log/slog"
)

// logWriter sends GORM warnings (slow queries, errors) to slog.
type logWriter struct{}

func (logWriter) Printf(format string, args ...any) {
	slog.Warn("db", "msg", fmt.Sprintf(format, args...))
}
