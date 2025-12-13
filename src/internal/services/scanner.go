package services

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type LibraryScanner struct {
	repo ports.MediaRepository
}

func NewLibraryScanner(repo ports.MediaRepository) *LibraryScanner {
	return &LibraryScanner{repo: repo}
}

// ScanDirectory walks the given root path and adds video files to the repository
func (s *LibraryScanner) ScanDirectory(ctx context.Context, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		if isVideoFile(path) {
			item := mapFileToMediaItem(root, path, info)
			if err := s.repo.Save(ctx, &item); err != nil {
				return fmt.Errorf("failed to save item %s: %w", item.Title, err)
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

func mapFileToMediaItem(root, path string, info os.FileInfo) domain.MediaItem {
	// Simple ID generation based on path hash
	hash := md5.Sum([]byte(path))
	id := hex.EncodeToString(hash[:])

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
