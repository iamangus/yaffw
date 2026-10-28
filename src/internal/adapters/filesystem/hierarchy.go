package filesystem

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var videoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".webm": true,
	".mov":  true,
	".m4v":  true,
	".wmv":  true,
	".flv":  true,
}

var episodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)S(\d+)E(\d+)`),
	regexp.MustCompile(`(?i)(\d+)x(\d+)`),
}

var seasonDirPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^Season\s*(\d+)$`),
	regexp.MustCompile(`(?i)^Season(\d+)$`),
	regexp.MustCompile(`(?i)^S(\d+)$`),
}

var yearPattern = regexp.MustCompile(`\s*\(\d{4}\)\s*`)

var qualityTags = []string{
	"480p", "720p", "1080p", "2160p", "4K",
	"WEB-DL", "WEBRip", "BluRay", "BDRip", "DVDRip",
	"x264", "x265", "h264", "h265", "HEVC",
	"AAC", "AC3", "DTS",
	"REPACK", "PROPER", "INTERNAL",
}

func IsVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return videoExtensions[ext]
}

func ParseEpisodeNumber(filename string) (season, episode int) {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	for _, p := range episodePatterns {
		matches := p.FindStringSubmatch(stem)
		if len(matches) == 3 {
			season, _ = strconv.Atoi(matches[1])
			episode, _ = strconv.Atoi(matches[2])
			return season, episode
		}
	}
	return 0, 0
}

func ParseSeasonDir(dirname string) int {
	if strings.EqualFold(dirname, "Specials") {
		return 0
	}
	for _, p := range seasonDirPatterns {
		matches := p.FindStringSubmatch(dirname)
		if len(matches) == 2 {
			n, _ := strconv.Atoi(matches[1])
			return n
		}
	}
	return -1
}

func IsSeasonDir(dirname string) bool {
	return ParseSeasonDir(dirname) >= 0
}

func ParseTitleFromDir(dirname string) string {
	title := yearPattern.ReplaceAllString(dirname, "")
	title = strings.TrimSpace(title)
	return title
}

func ParseTitleFromFilename(filename string) string {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))

	for _, p := range episodePatterns {
		stem = p.ReplaceAllString(stem, "")
	}

	for _, tag := range qualityTags {
		stem = regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(tag)+`\b`).ReplaceAllString(stem, "")
	}

	stem = regexp.MustCompile(`[._]`).ReplaceAllString(stem, " ")
	stem = regexp.MustCompile(`\s+`).ReplaceAllString(stem, " ")
	stem = strings.TrimSpace(stem)

	return stem
}

func ParseSeriesTitleFromPath(path string, libraryRoot string) string {
	relPath, err := filepath.Rel(libraryRoot, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return ""
	}
	seriesDir := parts[0]
	return ParseTitleFromDir(seriesDir)
}
