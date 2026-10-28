package nfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
)

const sampleMovieNFO = `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>The Matrix</title>
  <originaltitle>The Matrix</originaltitle>
  <plot>A computer hacker learns from mysterious rebels about the true nature of his reality.</plot>
  <tagline>Welcome to the real world.</tagline>
  <rating>8.7</rating>
  <year>1999</year>
  <runtime>136</runtime>
  <genre>Sci-Fi</genre>
  <genre>Action</genre>
  <country>USA</country>
  <studio>Warner Bros.</studio>
  <director>Lana Wachowski</director>
  <director>Lilly Wachowski</director>
  <writer>Lana Wachowski</writer>
  <mpaa>R</mpaa>
  <actor><name>Keanu Reeves</name><role>Neo</role></actor>
  <actor><name>Laurence Fishburne</name><role>Morpheus</role></actor>
  <uniqueid type="tmdb">603</uniqueid>
  <uniqueid type="imdb">tt0133093</uniqueid>
</movie>`

func TestParseNFO_Movie(t *testing.T) {
	meta, err := ParseNFOReader(strings.NewReader(sampleMovieNFO))
	if err != nil {
		t.Fatalf("ParseNFOReader failed: %v", err)
	}
	if meta.Title != "The Matrix" {
		t.Errorf("Title = %q, want %q", meta.Title, "The Matrix")
	}
	if meta.OriginalTitle != "The Matrix" {
		t.Errorf("OriginalTitle = %q, want %q", meta.OriginalTitle, "The Matrix")
	}
	if meta.Tagline != "Welcome to the real world." {
		t.Errorf("Tagline = %q, want %q", meta.Tagline, "Welcome to the real world.")
	}
	if meta.Rating != 8.7 {
		t.Errorf("Rating = %v, want 8.7", meta.Rating)
	}
	if meta.Certification != "R" {
		t.Errorf("Certification = %q, want %q", meta.Certification, "R")
	}
	if meta.ReleaseDate.Year() != 1999 {
		t.Errorf("ReleaseDate year = %d, want 1999", meta.ReleaseDate.Year())
	}
	if meta.Duration != 136*time.Minute {
		t.Errorf("Duration = %v, want %v", meta.Duration, 136*time.Minute)
	}
	if len(meta.Genres) != 2 {
		t.Fatalf("Genres len = %d, want 2", len(meta.Genres))
	}
	if len(meta.Directors) != 2 {
		t.Fatalf("Directors len = %d, want 2", len(meta.Directors))
	}
	if len(meta.Cast) != 2 {
		t.Fatalf("Cast len = %d, want 2", len(meta.Cast))
	}
	if meta.Cast[0].Name != "Keanu Reeves" || meta.Cast[0].Role != "Neo" {
		t.Errorf("Cast[0] = %+v, want {Name:Keanu Reeves Role:Neo}", meta.Cast[0])
	}
	if meta.ExternalIDs["tmdb"] != "603" {
		t.Errorf("ExternalIDs[tmdb] = %q, want %q", meta.ExternalIDs["tmdb"], "603")
	}
	if meta.OriginalID != "603" {
		t.Errorf("OriginalID = %q, want %q", meta.OriginalID, "603")
	}
}

func TestParseNFO_Minimal(t *testing.T) {
	meta, err := ParseNFOReader(strings.NewReader(`<movie><title>Test</title></movie>`))
	if err != nil {
		t.Fatalf("ParseNFOReader failed: %v", err)
	}
	if meta.Title != "Test" {
		t.Errorf("Title = %q, want %q", meta.Title, "Test")
	}
}

func TestParseNFO_MalformedXML(t *testing.T) {
	_, err := ParseNFOReader(strings.NewReader(`<movie><title>Unclosed`))
	if err == nil {
		t.Error("Expected error for malformed XML, got nil")
	}
}

func TestFindNFOFile_Movie(t *testing.T) {
	dir := t.TempDir()
	nfoPath := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte("<movie><title>Test</title></movie>"), 0644); err != nil {
		t.Fatal(err)
	}
	item := &domain.MediaItem{
		Type: domain.MediaTypeMovie,
		Path: filepath.Join(dir, "test.mkv"),
	}
	found, err := FindNFOFile(item)
	if err != nil {
		t.Fatalf("FindNFOFile failed: %v", err)
	}
	if found != nfoPath {
		t.Errorf("FindNFOFile = %q, want %q", found, nfoPath)
	}
}

func TestFindNFOFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	item := &domain.MediaItem{
		Type: domain.MediaTypeMovie,
		Path: filepath.Join(dir, "test.mkv"),
	}
	_, err := FindNFOFile(item)
	if err != ErrNoNFO {
		t.Errorf("FindNFOFile error = %v, want ErrNoNFO", err)
	}
}

func TestFindSidecarImage_Poster(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	item := &domain.MediaItem{
		Type: domain.MediaTypeMovie,
		Path: filepath.Join(dir, "test.mkv"),
	}
	if got := FindSidecarImage(item, "poster"); got != "poster.jpg" {
		t.Errorf("FindSidecarImage poster = %q, want %q", got, "poster.jpg")
	}
	if got := FindSidecarImage(item, "backdrop"); got != "" {
		t.Errorf("FindSidecarImage backdrop = %q, want empty", got)
	}
}
