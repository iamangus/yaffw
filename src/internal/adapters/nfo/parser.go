package nfo

import (
	"encoding/xml"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

// nfoRoot is the XML structure of a Kodi/Emby NFO file.
// Lenient: unknown elements are ignored, missing fields default to zero values.
type nfoRoot struct {
	XMLName       xml.Name      `xml:"movie,omitempty" json:"-"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle"`
	Plot          string        `xml:"plot"`
	Outline       string        `xml:"outline"`
	Tagline       string        `xml:"tagline"`
	Rating        float64       `xml:"rating"`
	Year          string        `xml:"year"`
	Runtime       string        `xml:"runtime"`
	Genres        []string      `xml:"genre"`
	Countries     []string      `xml:"country"`
	Studios       []string      `xml:"studio"`
	Directors     []string      `xml:"director"`
	Writers       []string      `xml:"writer"`
	MPAA          string        `xml:"mpaa"`
	Actors        []nfoActor    `xml:"actor"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
}

type nfoActor struct {
	Name string `xml:"name"`
	Role string `xml:"role"`
}

type nfoUniqueID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// ParseNFO reads and parses an NFO file from the given path.
func ParseNFO(path string) (*domain.MediaMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseNFOReader(f)
}

// ParseNFOReader parses NFO XML from any io.Reader.
func ParseNFOReader(r io.Reader) (*domain.MediaMetadata, error) {
	var root nfoRoot
	decoder := xml.NewDecoder(r)
	decoder.Strict = false
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	return rootToMetadata(&root), nil
}

func rootToMetadata(root *nfoRoot) *domain.MediaMetadata {
	meta := &domain.MediaMetadata{
		Title:         strings.TrimSpace(root.Title),
		OriginalTitle: strings.TrimSpace(root.OriginalTitle),
		Overview:      strings.TrimSpace(root.Plot),
		Tagline:       strings.TrimSpace(root.Tagline),
		Rating:        root.Rating,
		Genres:        root.Genres,
		Countries:     root.Countries,
		Studios:       root.Studios,
		Directors:     root.Directors,
		Writers:       root.Writers,
		Certification: strings.TrimSpace(root.MPAA),
	}

	if root.Year != "" {
		if t, err := time.Parse("2006", root.Year); err == nil {
			meta.ReleaseDate = t
		}
	}

	if root.Runtime != "" {
		if mins, err := strconv.Atoi(root.Runtime); err == nil {
			meta.Duration = time.Duration(mins) * time.Minute
		}
	}

	if len(root.Actors) > 0 {
		meta.Cast = make([]domain.CastMember, 0, len(root.Actors))
		for _, a := range root.Actors {
			meta.Cast = append(meta.Cast, domain.CastMember{
				Name: strings.TrimSpace(a.Name),
				Role: strings.TrimSpace(a.Role),
			})
		}
	}

	if len(root.UniqueIDs) > 0 {
		meta.ExternalIDs = make(map[string]string, len(root.UniqueIDs))
		for _, id := range root.UniqueIDs {
			if id.Type != "" && id.Value != "" {
				meta.ExternalIDs[id.Type] = id.Value
			}
		}
		if tmdb, ok := meta.ExternalIDs["tmdb"]; ok {
			meta.OriginalID = tmdb
		} else if imdb, ok := meta.ExternalIDs["imdb"]; ok {
			meta.OriginalID = imdb
		}
	}

	return meta
}
