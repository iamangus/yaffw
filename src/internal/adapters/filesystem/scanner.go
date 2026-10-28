package filesystem

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yaffw/yaffw/src/internal/config"
	"github.com/yaffw/yaffw/src/internal/domain"
	"github.com/yaffw/yaffw/src/internal/ports"
	"github.com/yaffw/yaffw/src/internal/services"
)

type Scanner struct {
	library      config.LibraryConfig
	provider     ports.MetadataProvider
	identityRepo ports.IdentityRepository
	mediaStore   *services.MediaStore
}

func NewScanner(lib config.LibraryConfig, provider ports.MetadataProvider, identityRepo ports.IdentityRepository, store *services.MediaStore) *Scanner {
	return &Scanner{
		library:      lib,
		provider:     provider,
		identityRepo: identityRepo,
		mediaStore:   store,
	}
}

func (s *Scanner) Scan(ctx context.Context) error {
	log.Printf("[Scanner] Starting scan for library '%s' at %s", s.library.Name, s.library.Path)

	seenUUIDs := make(map[string]bool)

	switch s.library.Type {
	case "movie":
		if err := s.scanMovies(ctx, seenUUIDs); err != nil {
			return err
		}
	case "series":
		if err := s.scanSeries(ctx, seenUUIDs); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown library type: %s", s.library.Type)
	}

	s.markMissing(ctx, seenUUIDs)

	log.Printf("[Scanner] Scan complete for library '%s': %d items found", s.library.Name, len(seenUUIDs))
	return nil
}

func (s *Scanner) scanMovies(ctx context.Context, seenUUIDs map[string]bool) error {
	entries, err := os.ReadDir(s.library.Path)
	if err != nil {
		return fmt.Errorf("failed to read library directory %s: %w", s.library.Path, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if err := s.scanMovieDir(ctx, filepath.Join(s.library.Path, entry.Name()), seenUUIDs); err != nil {
				log.Printf("[Scanner] Error scanning movie dir %s: %v", entry.Name(), err)
			}
		} else if IsVideoFile(entry.Name()) {
			if err := s.scanMovieFile(ctx, filepath.Join(s.library.Path, entry.Name()), seenUUIDs); err != nil {
				log.Printf("[Scanner] Error scanning movie file %s: %v", entry.Name(), err)
			}
		}
	}
	return nil
}

func (s *Scanner) scanMovieDir(ctx context.Context, dirPath string, seenUUIDs map[string]bool) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}

	var videoFiles []string
	for _, e := range entries {
		if !e.IsDir() && IsVideoFile(e.Name()) {
			videoFiles = append(videoFiles, e.Name())
		}
	}

	if len(videoFiles) == 0 {
		return nil
	}

	dirName := filepath.Base(dirPath)
	movieTitle := ParseTitleFromDir(dirName)

	if len(videoFiles) == 1 {
		filePath := filepath.Join(dirPath, videoFiles[0])
		info := ports.HierarchyInfo{
			LibraryType: "movie",
			FileName:    videoFiles[0],
		}
		item, err := s.resolveItem(ctx, filePath, info, movieTitle, domain.MediaTypeMovie, "")
		if err != nil {
			return err
		}
		s.mediaStore.AddOrUpdate(item)
		seenUUIDs[item.ID] = true
	} else {
		parentInfo := ports.HierarchyInfo{
			LibraryType: "movie",
			FileName:    dirName,
		}
		parentItem, err := s.resolveItem(ctx, dirPath, parentInfo, movieTitle, domain.MediaTypeMovie, "")
		if err != nil {
			return err
		}
		s.mediaStore.AddOrUpdate(parentItem)
		seenUUIDs[parentItem.ID] = true

		for _, vf := range videoFiles {
			filePath := filepath.Join(dirPath, vf)
			variantInfo := ports.HierarchyInfo{
				LibraryType: "movie",
				FileName:    vf,
			}
			variantTitle := movieTitle + " - " + strings.TrimSuffix(vf, filepath.Ext(vf))
			variantItem, err := s.resolveItem(ctx, filePath, variantInfo, variantTitle, domain.MediaTypeMovie, parentItem.ID)
			if err != nil {
				log.Printf("[Scanner] Error scanning variant %s: %v", vf, err)
				continue
			}
			s.mediaStore.AddOrUpdate(variantItem)
			seenUUIDs[variantItem.ID] = true
		}
	}

	return nil
}

func (s *Scanner) scanMovieFile(ctx context.Context, filePath string, seenUUIDs map[string]bool) error {
	fileName := filepath.Base(filePath)
	movieTitle := ParseTitleFromFilename(fileName)

	info := ports.HierarchyInfo{
		LibraryType: "movie",
		FileName:    fileName,
	}
	item, err := s.resolveItem(ctx, filePath, info, movieTitle, domain.MediaTypeMovie, "")
	if err != nil {
		return err
	}
	s.mediaStore.AddOrUpdate(item)
	seenUUIDs[item.ID] = true
	return nil
}

func (s *Scanner) scanSeries(ctx context.Context, seenUUIDs map[string]bool) error {
	entries, err := os.ReadDir(s.library.Path)
	if err != nil {
		return fmt.Errorf("failed to read series library %s: %w", s.library.Path, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		seriesDir := filepath.Join(s.library.Path, entry.Name())
		seriesTitle := ParseTitleFromDir(entry.Name())

		seriesInfo := ports.HierarchyInfo{
			LibraryType: "series",
			SeriesTitle: seriesTitle,
		}
		seriesItem, err := s.resolveItem(ctx, seriesDir, seriesInfo, seriesTitle, domain.MediaTypeSeries, "")
		if err != nil {
			log.Printf("[Scanner] Error scanning series %s: %v", entry.Name(), err)
			continue
		}
		s.mediaStore.AddOrUpdate(seriesItem)
		seenUUIDs[seriesItem.ID] = true

		s.scanSeriesChildren(ctx, seriesDir, seriesTitle, seriesItem.ID, seenUUIDs)
	}
	return nil
}

func (s *Scanner) scanSeriesChildren(ctx context.Context, seriesDir, seriesTitle, seriesUUID string, seenUUIDs map[string]bool) {
	entries, err := os.ReadDir(seriesDir)
	if err != nil {
		log.Printf("[Scanner] Error reading series dir %s: %v", seriesDir, err)
		return
	}

	seasonDirs := make(map[int]string)
	var directFiles []string

	for _, e := range entries {
		if e.IsDir() {
			seasonNum := ParseSeasonDir(e.Name())
			if seasonNum >= 0 {
				seasonDirs[seasonNum] = filepath.Join(seriesDir, e.Name())
			}
		} else if IsVideoFile(e.Name()) {
			directFiles = append(directFiles, e.Name())
		}
	}

	for seasonNum, seasonDir := range seasonDirs {
		seasonInfo := ports.HierarchyInfo{
			LibraryType:  "series",
			SeriesTitle:  seriesTitle,
			SeasonNumber: seasonNum,
		}
		seasonItem, err := s.resolveItem(ctx, seasonDir, seasonInfo,
			fmt.Sprintf("%s - Season %d", seriesTitle, seasonNum),
			domain.MediaTypeSeason, seriesUUID)
		if err != nil {
			log.Printf("[Scanner] Error scanning season %d: %v", seasonNum, err)
			continue
		}
		s.mediaStore.AddOrUpdate(seasonItem)
		seenUUIDs[seasonItem.ID] = true

		s.scanEpisodes(ctx, seasonDir, seriesTitle, seasonNum, seasonItem.ID, seenUUIDs)
	}

	for _, fileName := range directFiles {
		seasonNum, episodeNum := ParseEpisodeNumber(fileName)
		if episodeNum == 0 {
			continue
		}

		filePath := filepath.Join(seriesDir, fileName)
		epInfo := ports.HierarchyInfo{
			LibraryType:   "series",
			SeriesTitle:   seriesTitle,
			SeasonNumber:  seasonNum,
			EpisodeNumber: episodeNum,
			FileName:      fileName,
		}
		epTitle := fmt.Sprintf("%s S%02dE%02d", seriesTitle, seasonNum, episodeNum)
		epItem, err := s.resolveItem(ctx, filePath, epInfo, epTitle, domain.MediaTypeEpisode, seriesUUID)
		if err != nil {
			log.Printf("[Scanner] Error scanning episode %s: %v", fileName, err)
			continue
		}
		s.mediaStore.AddOrUpdate(epItem)
		seenUUIDs[epItem.ID] = true
	}
}

func (s *Scanner) scanEpisodes(ctx context.Context, seasonDir, seriesTitle string, seasonNum int, seasonUUID string, seenUUIDs map[string]bool) {
	entries, err := os.ReadDir(seasonDir)
	if err != nil {
		log.Printf("[Scanner] Error reading season dir %s: %v", seasonDir, err)
		return
	}

	for _, e := range entries {
		if e.IsDir() || !IsVideoFile(e.Name()) {
			continue
		}

		fileName := e.Name()
		_, episodeNum := ParseEpisodeNumber(fileName)
		if episodeNum == 0 {
			continue
		}

		filePath := filepath.Join(seasonDir, fileName)
		epInfo := ports.HierarchyInfo{
			LibraryType:   "series",
			SeriesTitle:   seriesTitle,
			SeasonNumber:  seasonNum,
			EpisodeNumber: episodeNum,
			FileName:      fileName,
		}
		epTitle := fmt.Sprintf("%s S%02dE%02d", seriesTitle, seasonNum, episodeNum)
		epItem, err := s.resolveItem(ctx, filePath, epInfo, epTitle, domain.MediaTypeEpisode, seasonUUID)
		if err != nil {
			log.Printf("[Scanner] Error scanning episode %s: %v", fileName, err)
			continue
		}
		s.mediaStore.AddOrUpdate(epItem)
		seenUUIDs[epItem.ID] = true
	}
}

func (s *Scanner) resolveItem(ctx context.Context, path string, info ports.HierarchyInfo, fallbackTitle string, mediaType domain.MediaType, parentID string) (*domain.MediaItem, error) {
	ph := PathHash(path)
	hk := HierarchyKey(info)

	record, err := s.identityRepo.GetByPathHash(ctx, ph)
	if err == nil {
		return s.updateExistingItem(record, path, ph, hk, parentID), nil
	}

	record, err = s.identityRepo.GetByHierarchyKey(ctx, hk)
	if err == nil {
		record.PathHash = ph
		record.LastSeen = time.Now()
		record.Status = "active"
		record.MissingSince = nil
		_ = s.identityRepo.Save(ctx, record)
		return s.updateExistingItem(record, path, ph, hk, parentID), nil
	}

	result, err := s.provider.Discover(ctx, path, info)
	if err != nil {
		log.Printf("[Scanner] Provider Discover failed for %s: %v, using hierarchy fallback", path, err)
		result = &ports.DiscoveryResult{
			Identity: hk,
			Title:    fallbackTitle,
		}
	}

	if result.Identity == "" {
		result.Identity = hk
	}
	if result.Title == "" {
		result.Title = fallbackTitle
	}

	record, err = s.identityRepo.GetByIdentity(ctx, result.Identity)
	if err == nil {
		record.PathHash = ph
		record.HierarchyKey = hk
		record.Title = result.Title
		record.LastSeen = time.Now()
		record.Status = "active"
		record.MissingSince = nil
		_ = s.identityRepo.Save(ctx, record)
		return s.buildItemFromRecord(record, path, parentID, result), nil
	}

	uuid := GenerateUUID(result.Identity)
	now := time.Now()
	newRecord := &ports.IdentityRecord{
		UUID:         uuid,
		LibraryID:    s.library.Name,
		PathHash:     ph,
		Identity:     result.Identity,
		HierarchyKey: hk,
		Title:        result.Title,
		LastSeen:     now,
		Status:       "active",
	}
	_ = s.identityRepo.Save(ctx, newRecord)

	return s.buildItemFromRecord(newRecord, path, parentID, result), nil
}

func (s *Scanner) updateExistingItem(record *ports.IdentityRecord, path, ph, hk string, parentID string) *domain.MediaItem {
	record.LastSeen = time.Now()
	record.Status = "active"
	record.MissingSince = nil
	if record.PathHash != ph {
		record.PathHash = ph
	}
	if record.HierarchyKey != hk && hk != "" {
		record.HierarchyKey = hk
	}
	_ = s.identityRepo.Save(context.Background(), record)

	return &domain.MediaItem{
		ID:         record.UUID,
		LibraryID:  s.library.Name,
		ParentID:   parentID,
		Path:       path,
		Title:      record.Title,
		Type:       s.mediaTypeForRecord(record),
		Identity:   record.Identity,
		PosterURL:  s.generatePosterURL(record.UUID, record.Identity),
		BackURL:    s.generateBackdropURL(record.UUID, record.Identity),
		CreatedAt:  record.LastSeen,
		Attributes: s.buildAttributes(record),
	}
}

func (s *Scanner) buildItemFromRecord(record *ports.IdentityRecord, path, parentID string, result *ports.DiscoveryResult) *domain.MediaItem {
	return &domain.MediaItem{
		ID:         record.UUID,
		LibraryID:  s.library.Name,
		ParentID:   parentID,
		Path:       path,
		Title:      record.Title,
		Type:       s.mediaTypeForRecord(record),
		Identity:   record.Identity,
		PosterURL:  s.generatePosterURL(record.UUID, record.Identity),
		BackURL:    s.generateBackdropURL(record.UUID, record.Identity),
		CreatedAt:  time.Now(),
		Attributes: s.buildAttributes(record),
	}
}

func (s *Scanner) mediaTypeForRecord(record *ports.IdentityRecord) domain.MediaType {
	hk := record.HierarchyKey
	switch {
	case strings.HasPrefix(hk, "series:"):
		if strings.Contains(hk, ":episode:") {
			return domain.MediaTypeEpisode
		}
		if strings.Contains(hk, ":season:") {
			return domain.MediaTypeSeason
		}
		return domain.MediaTypeSeries
	case strings.HasPrefix(hk, "movie:"):
		return domain.MediaTypeMovie
	default:
		return domain.MediaTypeMovie
	}
}

func (s *Scanner) buildAttributes(record *ports.IdentityRecord) map[string]interface{} {
	hk := record.HierarchyKey
	attrs := make(map[string]interface{})

	if strings.HasPrefix(hk, "series:") {
		parts := strings.Split(hk, ":")
		if len(parts) >= 4 && parts[2] == "season" {
			attrs["season"] = parts[3]
			if len(parts) >= 6 && parts[4] == "episode" {
				attrs["episode"] = parts[5]
			}
		}
	}
	return attrs
}

func (s *Scanner) generatePosterURL(uuid, identity string) string {
	switch s.library.MetadataProvider {
	case "omniarr":
		entityUUID := strings.TrimPrefix(identity, "omniarr:")
		if entityUUID == identity {
			return ""
		}
		return "/images/" + s.library.Name + "/" + entityUUID + "_poster.jpg"
	default:
		return "/local-images/" + uuid + "/poster.jpg"
	}
}

func (s *Scanner) generateBackdropURL(uuid, identity string) string {
	switch s.library.MetadataProvider {
	case "omniarr":
		entityUUID := strings.TrimPrefix(identity, "omniarr:")
		if entityUUID == identity {
			return ""
		}
		return "/images/" + s.library.Name + "/" + entityUUID + "_backdrop.jpg"
	default:
		return "/local-images/" + uuid + "/fanart.jpg"
	}
}

func (s *Scanner) markMissing(ctx context.Context, seenUUIDs map[string]bool) {
	all, err := s.identityRepo.ListAll(ctx)
	if err != nil {
		log.Printf("[Scanner] Error listing identity records for missing check: %v", err)
		return
	}

	for _, record := range all {
		if record.LibraryID != s.library.Name {
			continue
		}
		if !seenUUIDs[record.UUID] && record.Status == "active" {
			log.Printf("[Scanner] Marking item %s (%s) as missing", record.Title, record.UUID)
			_ = s.identityRepo.MarkMissing(ctx, record.UUID)
		}
	}
}
