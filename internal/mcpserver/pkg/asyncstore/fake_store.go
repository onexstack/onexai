package asyncstore

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic" // Used for atomic operations on the map

	"github.com/brianvoe/gofakeit/v7"

	v1 "github.com/onexstack/onexai/pkg/api/mcpserver/v1"
)

// FakeStore implements an in-memory storage mechanism for FakeData.
// It uses atomic.Value to ensure concurrent-safe and consistent access
// during data synchronization.
type FakeStore struct {
	mu   sync.RWMutex
	data atomic.Value // Stores map[string]*v1.FakeData
	// A mutex might still be needed for other operations not covered by atomic.Value,
	// or for protecting other fields of FakeStore.
	// For just replacing the map, atomic.Value is sufficient and better.
}

// NewFakeStore creates a new instance of FakeStore.
func NewFakeStore() *FakeStore {
	fs := &FakeStore{}
	fs.data.Store(make(map[string]*v1.FakeData)) // Initialize with an empty map
	return fs
}

// Sync simulates data synchronization by generating random fake data and
// atomically updating the store.
func (s *FakeStore) Sync(ctx context.Context) error {
	slog.InfoContext(ctx, "starting fake data synchronization")

	newItems := make(map[string]*v1.FakeData)
	const numItems = 10 // Number of random items to generate
	for i := 0; i < numItems; i++ {
		id := gofakeit.UUID()
		newItems[id] = &v1.FakeData{
			ID:          id,
			Name:        gofakeit.AppName(),
			Category:    gofakeit.CarMaker(),
			Description: gofakeit.Phrase(),
			Status:      gofakeit.RandomString([]string{"active", "pending", "failed"}),
			Score:       gofakeit.Float64Range(0.0, 100.0),
		}
	}

	// Add a fixed item for deterministic testing.
	fixedID := "fixed-item-001"
	newItems[fixedID] = &v1.FakeData{
		ID:          fixedID,
		Name:        "Fixed Test Item",
		Category:    "Testing",
		Description: "This item always exists.",
		Status:      "active",
		Score:       99.9,
	}

	// Atomically replace the entire map with the new data.
	s.data.Store(newItems)

	slog.InfoContext(ctx, "fake data synchronization completed", "items_generated", len(newItems))
	return nil
}

// Get retrieves an item by its ID.
// It returns the item and a boolean indicating if the item was found.
func (s *FakeStore) Get(id string) (*v1.FakeData, bool) {
	currentData := s.data.Load().(map[string]*v1.FakeData) // Load the current map atomically
	item, ok := currentData[id]
	return item, ok
}

// List retrieves all items currently in the store.
// It returns a slice of pointers to FakeData.
func (s *FakeStore) List() []*v1.FakeData {
	currentData := s.data.Load().(map[string]*v1.FakeData) // Load the current map atomically

	list := make([]*v1.FakeData, 0, len(currentData))
	for _, item := range currentData {
		list = append(list, item)
	}
	return list
}
