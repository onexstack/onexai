package watcher

import (
	"github.com/onexstack/onexstack/pkg/watch/initializer"
	"github.com/onexstack/onexstack/pkg/watch/registry"
)

// WatcherInitializer is used for initialization of the onex specific watcher plugins.
type WatcherInitializer struct {
	*AggregateConfig
}

// Ensure that WatcherInitializer implements the initializer.WatcherInitializer interface.
var _ initializer.WatcherInitializer = &WatcherInitializer{}

// NewInitializer creates and returns a new WatcherInitializer instance.
func NewInitializer(aggregate *AggregateConfig) *WatcherInitializer {
	return &WatcherInitializer{AggregateConfig: aggregate}
}

// Initialize configures the provided watcher by injecting dependencies
// such as the Store and AggregateConfig when supported by the watcher.
func (w *WatcherInitializer) Initialize(wc registry.Watcher) {
	if wants, ok := wc.(WantsStore); ok {
		wants.SetStore(w.Store)
	}

	if wants, ok := wc.(WantsAggregateConfig); ok {
		wants.SetAggregateConfig(w.AggregateConfig)
	}
}
