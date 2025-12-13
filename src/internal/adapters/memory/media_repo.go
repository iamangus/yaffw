package memory

import (
	"context"
	"errors"
	"sync"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type InMemoryMediaRepo struct {
	items map[string]domain.MediaItem
	mu    sync.RWMutex
}

func NewMediaRepo() *InMemoryMediaRepo {
	return &InMemoryMediaRepo{
		items: make(map[string]domain.MediaItem),
	}
}

func (r *InMemoryMediaRepo) GetByID(ctx context.Context, id string) (*domain.MediaItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.items[id]
	if !ok {
		return nil, errors.New("item not found")
	}
	return &item, nil
}

func (r *InMemoryMediaRepo) ListAll(ctx context.Context) ([]domain.MediaItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]domain.MediaItem, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	return items, nil
}

func (r *InMemoryMediaRepo) Save(ctx context.Context, item *domain.MediaItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.items[item.ID] = *item
	return nil
}
