package nfo

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/yaffw/yaffw/src/internal/domain"
)

var ErrNoNFO = errors.New("no NFO file found")

// FindNFOFile locates the NFO file for a media item based on its type and path.
// Returns the absolute path to the NFO file, or ErrNoNFO if none found.
func FindNFOFile(item *domain.MediaItem) (string, error) {
	if item.Path == "" {
		return "", ErrNoNFO
	}

	dir := filepath.Dir(item.Path)
	basename := filepath.Base(item.Path)
	ext := filepath.Ext(basename)
	stem := basename[:len(basename)-len(ext)]

	var candidates []string

	switch item.Type {
	case domain.MediaTypeMovie:
		candidates = []string{
			filepath.Join(dir, "movie.nfo"),
			filepath.Join(dir, stem+".nfo"),
		}
	case domain.MediaTypeSeries:
		candidates = []string{
			filepath.Join(dir, "tvshow.nfo"),
		}
	case domain.MediaTypeSeason:
		candidates = []string{
			filepath.Join(dir, "season.nfo"),
			filepath.Join(dir, "season.nfo"),
		}
		if seasonNum := getSeasonNumber(item); seasonNum >= 0 {
			candidates = append(candidates, filepath.Join(dir, "season"+pad2(seasonNum)+".nfo"))
		}
	case domain.MediaTypeEpisode:
		candidates = []string{
			filepath.Join(dir, stem+".nfo"),
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
			return c, nil
		}
	}

	return "", ErrNoNFO
}

// FindSidecarImage looks for poster/backdrop images in the same directory as the media file.
// Returns the filename (not full path) of the first image found, or "" if none.
func FindSidecarImage(item *domain.MediaItem, imageType string) string {
	if item.Path == "" {
		return ""
	}

	dir := filepath.Dir(item.Path)

	var candidates []string
	switch imageType {
	case "poster":
		candidates = []string{"poster.jpg", "poster.png", "folder.jpg", "default.tbn"}
	case "backdrop":
		candidates = []string{"fanart.jpg", "fanart.png"}
	default:
		return ""
	}

	for _, c := range candidates {
		full := filepath.Join(dir, c)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			return c
		}
	}

	return ""
}

func getSeasonNumber(item *domain.MediaItem) int {
	if item.Attributes == nil {
		return -1
	}
	if v, ok := item.Attributes["season"]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case string:
			if i, err := strconv.Atoi(n); err == nil {
				return i
			}
		}
	}
	return -1
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
