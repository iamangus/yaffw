package omniarr

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type OmniarrMetadataProvider struct {
	client      *OmniarrClient
	libraryName string
}

func NewOmniarrMetadataProvider(client *OmniarrClient, libraryName string) *OmniarrMetadataProvider {
	return &OmniarrMetadataProvider{client: client, libraryName: libraryName}
}

func (p *OmniarrMetadataProvider) Discover(ctx context.Context, path string, info ports.HierarchyInfo) (*ports.DiscoveryResult, error) {
	if info.LibraryType == "series" && info.EpisodeNumber > 0 {
		return &ports.DiscoveryResult{}, nil
	}
	if info.LibraryType == "series" && info.SeasonNumber > 0 && info.EpisodeNumber == 0 {
		return &ports.DiscoveryResult{}, nil
	}

	searchTitle := info.SeriesTitle
	if searchTitle == "" {
		if info.FileName != "" {
			searchTitle = strings.TrimSuffix(info.FileName, filepath.Ext(info.FileName))
		} else {
			searchTitle = filepath.Base(path)
			if isDir, _ := isDirectory(path); isDir {
				searchTitle = filepath.Base(path)
			}
		}
	}
	searchTitle = cleanTitleForSearch(searchTitle)
	if searchTitle == "" {
		return &ports.DiscoveryResult{}, nil
	}

	entityType := "movie"
	if info.LibraryType == "series" {
		entityType = "series"
	}

	entities, err := p.client.SearchEntities(searchTitle, entityType)
	if err != nil {
		log.Printf("[OmniarrProvider] Search failed for '%s': %v", searchTitle, err)
		return &ports.DiscoveryResult{}, nil
	}

	if len(entities) == 0 {
		log.Printf("[OmniarrProvider] No results for '%s'", searchTitle)
		return &ports.DiscoveryResult{}, nil
	}

	entity := entities[0]
	identity := "omniarr:" + entity.UUID
	title := entity.Metadata["title"].(string)

	result := &ports.DiscoveryResult{
		Identity: identity,
		Title:    title,
	}

	if runtimeRaw, ok := entity.Metadata["runtime"]; ok {
		if runtime, ok := runtimeRaw.(float64); ok {
			result.Duration = time.Duration(runtime) * time.Minute
		}
	}

	return result, nil
}

func (p *OmniarrMetadataProvider) FetchMetadata(ctx context.Context, item *domain.MediaItem) (*domain.MediaMetadata, error) {
	entityUUID := strings.TrimPrefix(item.Identity, "omniarr:")
	if entityUUID == item.Identity {
		return &domain.MediaMetadata{Title: item.Title}, nil
	}

	log.Printf("[OmniarrProvider] Fetching details for %s (entity %s)", item.Title, entityUUID)

	entity, err := p.client.GetEntityDetails(entityUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch entity from omniarr: %w", err)
	}

	metadata := &domain.MediaMetadata{}

	if title, ok := entity.Metadata["title"].(string); ok {
		metadata.Title = title
	}
	if desc, ok := entity.Metadata["description"].(string); ok {
		metadata.Overview = desc
	}
	if yearStr, ok := entity.Metadata["year"].(string); ok {
		if t, err := time.Parse("2006", yearStr); err == nil {
			metadata.ReleaseDate = t
		}
	}

	getFromMetaOrExtra := func(key string) interface{} {
		if v, ok := entity.Metadata[key]; ok {
			return v
		}
		if extra, ok := entity.Metadata["extra"].(map[string]interface{}); ok {
			if v, ok := extra[key]; ok {
				return v
			}
		}
		return nil
	}

	if castRaw := getFromMetaOrExtra("cast"); castRaw != nil {
		if cast, ok := castRaw.([]interface{}); ok {
			for _, c := range cast {
				if m, ok := c.(map[string]interface{}); ok {
					name, _ := m["name"].(string)
					role, _ := m["role"].(string)
					metadata.Cast = append(metadata.Cast, domain.CastMember{Name: name, Role: role})
				}
			}
		}
	}

	if genresRaw := getFromMetaOrExtra("genres"); genresRaw != nil {
		if genres, ok := genresRaw.([]interface{}); ok {
			for _, g := range genres {
				if name, ok := g.(string); ok {
					metadata.Genres = append(metadata.Genres, name)
				}
			}
		}
	}

	if directorsRaw := getFromMetaOrExtra("directors"); directorsRaw != nil {
		if directors, ok := directorsRaw.([]interface{}); ok {
			for _, d := range directors {
				if name, ok := d.(string); ok {
					metadata.Directors = append(metadata.Directors, name)
				}
			}
		}
	}

	if writersRaw := getFromMetaOrExtra("writers"); writersRaw != nil {
		if writers, ok := writersRaw.([]interface{}); ok {
			for _, w := range writers {
				if name, ok := w.(string); ok {
					metadata.Writers = append(metadata.Writers, name)
				}
			}
		}
	}

	if tagline, ok := getFromMetaOrExtra("tagline").(string); ok {
		metadata.Tagline = tagline
	}

	if countriesRaw := getFromMetaOrExtra("countries"); countriesRaw != nil {
		if countries, ok := countriesRaw.([]interface{}); ok {
			for _, c := range countries {
				if name, ok := c.(string); ok {
					metadata.Countries = append(metadata.Countries, name)
				}
			}
		}
	}

	if studiosRaw := getFromMetaOrExtra("studios"); studiosRaw != nil {
		if studios, ok := studiosRaw.([]interface{}); ok {
			for _, s := range studios {
				if name, ok := s.(string); ok {
					metadata.Studios = append(metadata.Studios, name)
				}
			}
		}
	}

	if identifiers, ok := entity.Metadata["identifiers"].(map[string]interface{}); ok {
		metadata.ExternalIDs = make(map[string]string)
		if tmdb, ok := identifiers["tmdb"].(string); ok {
			metadata.ExternalIDs["tmdb"] = tmdb
			metadata.OriginalID = tmdb
		}
		if imdb, ok := identifiers["imdb"].(string); ok {
			metadata.ExternalIDs["imdb"] = imdb
			if metadata.OriginalID == "" {
				metadata.OriginalID = imdb
			}
		}
	}

	if runtimeRaw := getFromMetaOrExtra("runtime"); runtimeRaw != nil {
		if runtime, ok := runtimeRaw.(float64); ok {
			metadata.Duration = time.Duration(runtime) * time.Minute
		}
	}

	metadata.PosterImage = fmt.Sprintf("/images/%s/%s_poster.jpg", p.libraryName, entityUUID)
	metadata.BackdropURL = fmt.Sprintf("/images/%s/%s_backdrop.jpg", p.libraryName, entityUUID)

	if rating, ok := entity.Metadata["rating"].(float64); ok {
		metadata.Rating = rating
	} else if ratingStr, ok := entity.Metadata["rating"].(string); ok {
		if r, err := strconv.ParseFloat(ratingStr, 64); err == nil {
			metadata.Rating = r
		}
	}

	if metadata.Title == "" {
		metadata.Title = item.Title
	}

	return metadata, nil
}

func cleanTitleForSearch(title string) string {
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")
	for _, tag := range []string{"480p", "720p", "1080p", "2160p", "4K", "WEB-DL", "WEBRip", "BluRay", "BDRip", "x264", "x265", "HEVC", "AAC", "AC3", "DTS"} {
		title = strings.ReplaceAll(title, tag, "")
	}
	title = strings.TrimSpace(title)
	title = strings.Join(strings.Fields(title), " ")
	return title
}

func isDirectory(path string) (bool, error) {
	info, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	_ = info
	return true, nil
}
