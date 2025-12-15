package services

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type LibraryScanner struct {
	repo          ports.MediaRepository
	metadataQueue ports.MetadataQueue
	lockManager   ports.LockManager
}

func NewLibraryScanner(repo ports.MediaRepository, queue ports.MetadataQueue, lock ports.LockManager) *LibraryScanner {
	return &LibraryScanner{
		repo:          repo,
		metadataQueue: queue,
		lockManager:   lock,
	}
}

// StartDaemon runs the scanner in a loop, handling leader election
func (s *LibraryScanner) StartDaemon(ctx context.Context, root string, interval time.Duration) {
	log.Printf("[Scanner] Daemon started. Monitoring %s every %s", root, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial scan
	s.tryScan(ctx, root)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tryScan(ctx, root)
		}
	}
}

func (s *LibraryScanner) tryScan(ctx context.Context, root string) {
	// 1. Try to acquire lock
	lockKey := "scanner_leader"
	ttl := 300 // 5 minutes
	acquired, err := s.lockManager.TryAcquireLock(ctx, lockKey, ttl)
	if err != nil {
		log.Printf("[Scanner] Error acquiring lock: %v", err)
		return
	}
	if !acquired {
		// Log rarely to avoid spam
		// log.Printf("[Scanner] Standby (Lock held by another pod)")
		return
	}

	// 2. We are the leader!
	log.Println("[Scanner] Acquired lock. Starting scan...")
	start := time.Now()

	if err := s.ScanDirectory(ctx, root); err != nil {
		log.Printf("[Scanner] Error during scan: %v", err)
	}

	log.Printf("[Scanner] Scan completed in %s", time.Since(start))

	// We don't release the lock explicitly so we keep it until TTL expires or we renew it next loop.
	// Actually, if interval < TTL, we just extend it next time.
	// This prevents thrashing leadership.
}

// ScanDirectory walks the given root path and adds video files to the repository
func (s *LibraryScanner) ScanDirectory(ctx context.Context, root string) error {
	// Cache existing items to avoid DB thrashing?
	// For now, let's just check item-by-item or handle via "ON CONFLICT" + check result?
	// The repo.Save() is an UPSERT.
	// But we only want to enqueue metadata jobs for NEW items.
	// Repo.GetByID() is checking via ID (hash of path).

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		if isVideoFile(path) {
			// Generate ID
			hash := md5.Sum([]byte(path))
			id := hex.EncodeToString(hash[:])

			// Check if exists
			existing, err := s.repo.GetByID(ctx, id)
			if err == nil && existing != nil {
				// Item exists. Check if it needs metadata retry
				if existing.MetadataStatus == domain.MetadataStatusNew || existing.MetadataStatus == domain.MetadataStatusFailed {
					log.Printf("[Scanner] Item %s exists with status %s. Re-enqueuing metadata job.", existing.Title, existing.MetadataStatus)

					job := &domain.MetadataJob{
						ID:        "meta-" + id,
						MediaID:   id,
						Status:    "Pending",
						CreatedAt: time.Now(),
					}

					if err := s.metadataQueue.Enqueue(ctx, job); err != nil {
						log.Printf("[Scanner] Failed to re-enqueue metadata job for %s: %v", existing.Title, err)
					} else {
						// Update status to Pending to avoid spamming the queue
						existing.MetadataStatus = domain.MetadataStatusPending
						if err := s.repo.Save(ctx, existing); err != nil {
							log.Printf("[Scanner] Failed to update status for %s: %v", existing.Title, err)
						} else {
							log.Printf("[Scanner] Re-enqueued metadata job for %s", existing.Title)
						}
					}
				}
				return nil
			}

			// New Item
			item := mapFileToMediaItem(root, path, info, id)
			item.MetadataStatus = domain.MetadataStatusNew

			log.Printf("[Scanner] Found NEW video: %s", item.Title)
			if err := s.repo.Save(ctx, &item); err != nil {
				return fmt.Errorf("failed to save item %s: %w", item.Title, err)
			}

			// Enqueue Metadata Job
			job := &domain.MetadataJob{
				ID:        "meta-" + id,
				MediaID:   id,
				Status:    "Pending",
				CreatedAt: time.Now(),
			}
			if err := s.metadataQueue.Enqueue(ctx, job); err != nil {
				log.Printf("[Scanner] Failed to enqueue metadata job for %s: %v", item.Title, err)
			} else {
				log.Printf("[Scanner] Enqueued metadata job for %s", item.Title)
			}
		}
		return nil
	})
}

func isVideoFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm":
		return true
	}
	return false
}

func mapFileToMediaItem(root, path string, info os.FileInfo, id string) domain.MediaItem {
	// Naive title extraction (filename without ext)
	filename := filepath.Base(path)
	title := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Determine type based on folder structure (heuristic)
	// e.g. data/Movies/MovieName.mp4 vs data/Shows/ShowName/Episode.mp4
	mediaType := domain.MediaTypeMovie
	relPath, _ := filepath.Rel(root, path)
	if strings.Contains(strings.ToLower(relPath), "shows") || strings.Contains(strings.ToLower(relPath), "series") {
		mediaType = domain.MediaTypeEpisode
	}

	return domain.MediaItem{
		ID:        id,
		Title:     title,
		Type:      mediaType,
		Path:      path, // Absolute or relative path as scanned
		PosterURL: "",   // No metadata fetching yet
		Duration:  0,    // Requires ffprobe
		CreatedAt: time.Now(),
	}
}
