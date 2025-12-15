package services

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
)

type MetadataService struct {
	repo         ports.MediaRepository
	queue        ports.MetadataQueue
	provider     ports.MetadataProvider
	imageStore   ports.ImageStore
	workersCount int
}

func NewMetadataService(
	repo ports.MediaRepository,
	queue ports.MetadataQueue,
	provider ports.MetadataProvider,
	imageStore ports.ImageStore,
	workersCount int,
) *MetadataService {
	return &MetadataService{
		repo:         repo,
		queue:        queue,
		provider:     provider,
		imageStore:   imageStore,
		workersCount: workersCount,
	}
}

func (s *MetadataService) StartWorkers(ctx context.Context) {
	log.Printf("[Metadata] Starting %d workers...", s.workersCount)
	for i := 0; i < s.workersCount; i++ {
		go s.workerLoop(ctx, i)
	}
}

func (s *MetadataService) workerLoop(ctx context.Context, id int) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Dequeue
			job, err := s.queue.Dequeue(ctx)
			if err != nil {
				// Empty queue or db error
				continue
			}

			log.Printf("[MetadataWorker-%d] Processing job %s (Media: %s)", id, job.ID, job.MediaID)
			if err := s.processJob(ctx, job); err != nil {
				log.Printf("[MetadataWorker-%d] Job %s failed: %v", id, job.ID, err)
				s.queue.MarkFailed(ctx, job.ID, err.Error())
			} else {
				log.Printf("[MetadataWorker-%d] Job %s completed", id, job.ID)
				s.queue.MarkCompleted(ctx, job.ID)
			}
		}
	}
}

func (s *MetadataService) processJob(ctx context.Context, job *domain.MetadataJob) error {
	// 1. Get Media Item
	item, err := s.repo.GetByID(ctx, job.MediaID)
	if err != nil {
		return fmt.Errorf("media not found: %w", err)
	}

	// 2. Parse Filename (Title, Year)
	title, year := parseFilename(item.Title) // item.Title is actually filename-no-ext currently
	log.Printf("[Metadata] Searching: '%s' (%d) Type: %s", title, year, item.Type)

	// 3. Search Provider
	results, err := s.provider.Search(ctx, title, year, item.Type)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}
	if len(results) == 0 {
		return fmt.Errorf("no results found")
	}

	// 4. Get Details for best match (first result)
	match := results[0]
	details, err := s.provider.GetDetails(ctx, match.OriginalID, item.Type)
	if err != nil {
		return fmt.Errorf("details failed: %w", err)
	}

	// 5. Download Images
	if details.PosterImage != "" {
		// filename: mediaID_poster.jpg
		fname := fmt.Sprintf("%s_poster.jpg", item.ID)
		localURL, err := s.imageStore.Save(ctx, details.PosterImage, fname)
		if err == nil {
			item.PosterURL = localURL
		} else {
			log.Printf("[Metadata] Failed to download poster: %v", err)
		}
	}

	if details.BackdropURL != "" && strings.HasPrefix(details.BackdropURL, "http") {
		// filename: mediaID_backdrop.jpg
		fname := fmt.Sprintf("%s_backdrop.jpg", item.ID)
		localURL, err := s.imageStore.Save(ctx, details.BackdropURL, fname)
		if err == nil {
			details.BackdropURL = localURL // Update metadata to point to local
		} else {
			log.Printf("[Metadata] Failed to download backdrop: %v", err)
		}
	}

	// 6. Update Media Item
	item.Metadata = details
	item.MetadataStatus = domain.MetadataStatusReady
	if details.Title != "" {
		item.Title = details.Title
	}

	return s.repo.Save(ctx, item)
}

// Simple regex parser
// Matches "Movie Name (2023)" or "Movie.Name.2023.1080p"
func parseFilename(filename string) (string, int) {
	// 1. Try "Name (Year)"
	reYearParens := regexp.MustCompile(`^(.*)\s*\((\d{4})\)`)
	matches := reYearParens.FindStringSubmatch(filename)
	if len(matches) > 2 {
		return strings.TrimSpace(matches[1]), toInt(matches[2])
	}

	// 2. Try "Name.Year."
	reYearDot := regexp.MustCompile(`^(.*?)[\.\s](\d{4})[\.\s]`)
	matches = reYearDot.FindStringSubmatch(filename)
	if len(matches) > 2 {
		title := strings.ReplaceAll(matches[1], ".", " ")
		return strings.TrimSpace(title), toInt(matches[2])
	}

	// Fallback
	return filename, 0
}

func toInt(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
