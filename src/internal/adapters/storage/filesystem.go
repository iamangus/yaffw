package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type FilesystemImageStore struct {
	baseDir string
}

func NewFilesystemImageStore(baseDir string) (*FilesystemImageStore, error) {
	// Ensure base dir exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create artwork dir: %w", err)
	}
	return &FilesystemImageStore{baseDir: baseDir}, nil
}

func (s *FilesystemImageStore) Save(ctx context.Context, remoteURL string, filename string) (string, error) {
	if remoteURL == "" {
		return "", nil
	}

	// 1. Prepare Request
	req, err := http.NewRequestWithContext(ctx, "GET", remoteURL, nil)
	if err != nil {
		return "", err
	}

	// 2. Download
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status: %s", resp.Status)
	}

	// 3. Create File
	fullPath := filepath.Join(s.baseDir, filename)

	// Create subdirectories if filename contains paths
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	out, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// 4. Save
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	// 5. Return relative path (assuming served under /artwork/)
	// If the file was "movie.jpg", we return "/artwork/movie.jpg"
	// But the store shouldn't assume the HTTP prefix.
	// It should just return the filename relative to the root?
	// The interface contract says "public/relative URL".
	// Let's convention: we return "/artwork/" + filename
	// Ideally this prefix should be configurable or handled by the caller.
	// For now, hardcoding "/artwork/" is acceptable for MVP.

	// Normalize filename to ensure no double slashes
	relPath := "/artwork/" + strings.TrimPrefix(filename, "/")
	return relPath, nil
}
