package api

import (
	"sync/atomic"

	"github.com/normo/dota-collector/internal/config"
	"github.com/normo/dota-collector/internal/store"
)

type Backend struct {
	Store    *store.Store
	Config   *atomic.Pointer[config.Config]
	Stopping atomic.Bool
}
