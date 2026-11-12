package watcher

import (
	"github.com/onexstack/onexstack/pkg/watch/registry"

	"github.com/onexstack/onexai/internal/jobserver/store"
)

// WantsAggregateConfig defines a function which sets AggregateConfig for watcher plugins that need it.
type WantsAggregateConfig interface {
	registry.Watcher
	SetAggregateConfig(config *AggregateConfig)
}

type WantsStore interface {
	registry.Watcher
	SetStore(store store.IStore)
}
