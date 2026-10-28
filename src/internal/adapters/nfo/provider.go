package nfo

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type NFOMetadataProvider struct{}

func NewNFOMetadataProvider() *NFOMetadataProvider {
	return &NFOMetadataProvider{}
}

func (p *NFOMetadataProvider) Discover(ctx context.Context, path string, info ports.HierarchyInfo) (*ports.DiscoveryResult, error) {
	nfoPath := findNFOByHierarchy(path, info)
	if nfoPath == "" {
		return &ports.DiscoveryResult{}, nil
	}

	root, err := parseNFORaw(nfoPath)
	if err != nil {
		log.Printf("[NFOProvider] Failed to parse NFO %s: %v", nfoPath, err)
		return &ports.DiscoveryResult{}, nil
	}

	identity := extractIdentity(root.UniqueIDs)
	title := strings.TrimSpace(root.Title)

	result := &ports.DiscoveryResult{
		Identity: identity,
		Title:    title,
	}

	if root.Runtime != "" {
		if mins, err := strconv.Atoi(root.Runtime); err == nil {
			result.Duration = time.Duration(mins) * time.Minute
		}
	}

	return result, nil
}

func (p *NFOMetadataProvider) FetchMetadata(ctx context.Context, item *domain.MediaItem) (*domain.MediaMetadata, error) {
	nfoPath, err := FindNFOFile(item)
	if err != nil {
		if errors.Is(err, ErrNoNFO) {
			return nil, ErrNoNFO
		}
		return nil, fmt.Errorf("nfo: failed to find NFO file: %w", err)
	}

	meta, err := ParseNFO(nfoPath)
	if err != nil {
		return nil, fmt.Errorf("nfo: failed to parse %s: %w", nfoPath, err)
	}

	if poster := FindSidecarImage(item, "poster"); poster != "" {
		meta.PosterImage = fmt.Sprintf("/local-images/%s/%s", item.ID, poster)
	}
	if backdrop := FindSidecarImage(item, "backdrop"); backdrop != "" {
		meta.BackdropURL = fmt.Sprintf("/local-images/%s/%s", item.ID, backdrop)
	}

	if meta.Title == "" {
		meta.Title = item.Title
	}

	log.Printf("[NFOProvider] Loaded metadata for %s from %s (title=%s, genres=%d, cast=%d)",
		item.ID, nfoPath, meta.Title, len(meta.Genres), len(meta.Cast))

	return meta, nil
}

func findNFOByHierarchy(path string, info ports.HierarchyInfo) string {
	dir := filepath.Dir(path)
	basename := filepath.Base(path)
	ext := filepath.Ext(basename)
	stem := basename[:len(basename)-len(ext)]

	var candidates []string

	switch {
	case info.LibraryType == "movie":
		candidates = []string{
			filepath.Join(dir, "movie.nfo"),
			filepath.Join(dir, stem+".nfo"),
		}
	case info.LibraryType == "series" && info.EpisodeNumber > 0:
		candidates = []string{
			filepath.Join(dir, stem+".nfo"),
		}
	case info.LibraryType == "series" && info.SeasonNumber > 0:
		candidates = []string{
			filepath.Join(dir, "season.nfo"),
			filepath.Join(dir, "season"+pad2(info.SeasonNumber)+".nfo"),
		}
	case info.LibraryType == "series":
		candidates = []string{
			filepath.Join(dir, "tvshow.nfo"),
		}
	default:
		candidates = []string{
			filepath.Join(dir, stem+".nfo"),
			filepath.Join(dir, "movie.nfo"),
			filepath.Join(dir, "tvshow.nfo"),
		}
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}

	return ""
}

func extractIdentity(uniqueIDs []nfoUniqueID) string {
	priority := []string{"tmdb", "tvdb", "imdb"}
	idMap := make(map[string]string)
	for _, id := range uniqueIDs {
		if id.Type != "" && id.Value != "" {
			idMap[id.Type] = id.Value
		}
	}
	for _, p := range priority {
		if v, ok := idMap[p]; ok {
			return p + ":" + v
		}
	}
	return ""
}

func parseNFORaw(path string) (*nfoRoot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var root nfoRoot
	decoder := xml.NewDecoder(f)
	decoder.Strict = false
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	return &root, nil
}
