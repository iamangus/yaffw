package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/yaffw/yaffw/src/internal/ports"
)

func GenerateUUID(identity string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
}

func PathHash(absPath string) string {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		abs = absPath
	}
	h := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(h[:])
}

func HierarchyKey(info ports.HierarchyInfo) string {
	switch info.LibraryType {
	case "movie":
		return "movie:" + info.FileName
	case "series":
		if info.EpisodeNumber > 0 {
			return fmt.Sprintf("series:%s:season:%d:episode:%d",
				info.SeriesTitle, info.SeasonNumber, info.EpisodeNumber)
		}
		if info.SeasonNumber > 0 {
			return fmt.Sprintf("series:%s:season:%d",
				info.SeriesTitle, info.SeasonNumber)
		}
		return "series:" + info.SeriesTitle
	default:
		return "unknown:" + info.FileName
	}
}

func HierarchyKeyForMovie(fileName string) string {
	return "movie:" + fileName
}

func HierarchyKeyForSeries(seriesTitle string) string {
	return "series:" + seriesTitle
}

func HierarchyKeyForSeason(seriesTitle string, seasonNum int) string {
	return fmt.Sprintf("series:%s:season:%d", seriesTitle, seasonNum)
}

func HierarchyKeyForEpisode(seriesTitle string, seasonNum, episodeNum int) string {
	return fmt.Sprintf("series:%s:season:%d:episode:%d", seriesTitle, seasonNum, episodeNum)
}
