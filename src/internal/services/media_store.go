package services

import (
	"sync"

	"github.com/yaffw/yaffw/src/internal/domain"
)

type MediaStore struct {
	mu    sync.RWMutex
	items map[string]*domain.MediaItem
}

func NewMediaStore() *MediaStore {
	return &MediaStore{items: make(map[string]*domain.MediaItem)}
}

func (s *MediaStore) GetByID(id string) (*domain.MediaItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	return item, ok
}

func (s *MediaStore) ListByLibrary(libraryID string) []*domain.MediaItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*domain.MediaItem
	for _, item := range s.items {
		if item.LibraryID == libraryID {
			result = append(result, item)
		}
	}
	return result
}

func (s *MediaStore) ListAll() []*domain.MediaItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.MediaItem, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result
}

func (s *MediaStore) GetChildren(parentID string) []*domain.MediaItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*domain.MediaItem
	for _, item := range s.items {
		if item.ParentID == parentID {
			result = append(result, item)
		}
	}
	return result
}

func (s *MediaStore) AddOrUpdate(item *domain.MediaItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[item.ID] = item
}

func (s *MediaStore) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

func (s *MediaStore) ReplaceAll(items map[string]*domain.MediaItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
}

func (s *MediaStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}
