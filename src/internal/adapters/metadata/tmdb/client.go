package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

const baseURL = "https://api.themoviedb.org/3"

type TMDBClient struct {
	apiKey string
	client *http.Client
}

func NewTMDBClient(apiKey string) *TMDBClient {
	return &TMDBClient{
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Responses
type searchResult struct {
	Results []struct {
		ID           int    `json:"id"`
		Title        string `json:"title"`          // Movies
		Name         string `json:"name"`           // TV
		ReleaseDate  string `json:"release_date"`   // Movies
		FirstAirDate string `json:"first_air_date"` // TV
		Overview     string `json:"overview"`
		PosterPath   string `json:"poster_path"`
	} `json:"results"`
}

type detailsResponse struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	VoteAverage  float64 `json:"vote_average"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Genres       []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

func (c *TMDBClient) Search(ctx context.Context, title string, year int, mediaType domain.MediaType) ([]domain.MediaMetadata, error) {
	endpoint := "/search/movie"
	if mediaType == domain.MediaTypeSeries || mediaType == domain.MediaTypeEpisode {
		endpoint = "/search/tv"
	}

	u, _ := url.Parse(baseURL + endpoint)
	q := u.Query()
	q.Set("api_key", c.apiKey)
	q.Set("query", title)
	if year > 0 {
		if mediaType == domain.MediaTypeMovie {
			q.Set("primary_release_year", strconv.Itoa(year))
		} else {
			q.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("TMDB returned %d", resp.StatusCode)
	}

	var res searchResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	var metadata []domain.MediaMetadata
	for _, r := range res.Results {
		dateStr := r.ReleaseDate
		if dateStr == "" {
			dateStr = r.FirstAirDate
		}
		date, _ := time.Parse("2006-01-02", dateStr)

		title := r.Title
		if title == "" {
			title = r.Name
		}

		m := domain.MediaMetadata{
			OriginalID:  strconv.Itoa(r.ID),
			Overview:    r.Overview,
			ReleaseDate: date,
			// Rating/Genres not in basic search result usually, need details
		}
		// Temporarily store poster path in BackdropURL or we need a way to pass it?
		// Actually, we usually fetch details immediately after match.
		// Let's just return what we have.
		metadata = append(metadata, m)
	}
	return metadata, nil
}

func (c *TMDBClient) GetDetails(ctx context.Context, remoteID string, mediaType domain.MediaType) (*domain.MediaMetadata, error) {
	endpoint := fmt.Sprintf("/movie/%s", remoteID)
	if mediaType == domain.MediaTypeSeries || mediaType == domain.MediaTypeEpisode {
		endpoint = fmt.Sprintf("/tv/%s", remoteID)
	}

	u, _ := url.Parse(baseURL + endpoint)
	q := u.Query()
	q.Set("api_key", c.apiKey)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("TMDB returned %d", resp.StatusCode)
	}

	var d detailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}

	dateStr := d.ReleaseDate
	if dateStr == "" {
		dateStr = d.FirstAirDate
	}
	date, _ := time.Parse("2006-01-02", dateStr)

	var genres []string
	for _, g := range d.Genres {
		genres = append(genres, g.Name)
	}

	title := d.Title
	if title == "" {
		title = d.Name
	}

	meta := &domain.MediaMetadata{
		OriginalID:  strconv.Itoa(d.ID),
		Title:       title,
		Overview:    d.Overview,
		ReleaseDate: date,
		Rating:      d.VoteAverage,
		Genres:      genres,
	}

	if d.PosterPath != "" {
		meta.PosterImage = "https://image.tmdb.org/t/p/w500" + d.PosterPath
	}
	if d.BackdropPath != "" {
		meta.BackdropURL = "https://image.tmdb.org/t/p/original" + d.BackdropPath
	}

	return meta, nil
}

// Helper to get raw poster path if needed

func (c *TMDBClient) GetPosterURL(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/w500" + path
}

func (c *TMDBClient) GetBackdropURL(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/original" + path
}
