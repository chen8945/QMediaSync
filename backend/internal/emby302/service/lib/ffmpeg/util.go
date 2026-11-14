package ffmpeg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	durationReg    = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+\.\d+)`)
	streamReg      = regexp.MustCompile(`(?m)^[ \t]*Stream #[^\r\n]*`)
	albumReg       = regexp.MustCompile(`(?mi)^[ \t]*album[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	artistReg      = regexp.MustCompile(`(?mi)^[ \t]*artist[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	albumArtistReg = regexp.MustCompile(`(?mi)^[ \t]*album_artist[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	commentReg     = regexp.MustCompile(`(?mi)^[ \t]*comment[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	dateReg        = regexp.MustCompile(`(?mi)^[ \t]*date[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	titleReg       = regexp.MustCompile(`(?mi)^[ \t]*title[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	titleTiReg     = regexp.MustCompile(`(?mi):\s*\[ti:(.*?)\]\s*$`)
	trackReg       = regexp.MustCompile(`(?mi)^[ \t]*track[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	trackTotalReg  = regexp.MustCompile(`(?mi)^[ \t]*tracktotal[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	discReg        = regexp.MustCompile(`(?mi)^[ \t]*disc[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	discTotalReg   = regexp.MustCompile(`(?mi)^[ \t]*disctotal[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	genreReg       = regexp.MustCompile(`(?mi)^[ \t]*genre[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	tdorReg        = regexp.MustCompile(`(?mi)^[ \t]*tdor[ \t]*:[ \t]*([^\r\n]*?)[ \t]*\r?$`)
	lyricsReg      = regexp.MustCompile(`(?mi):\s*(\[.*?\].*?)\s*$`)
)

// resolveDuration 解析 FFmpeg 的 Duration 参数
func resolveDuration(raw string) time.Duration {
	if !durationReg.MatchString(raw) {
		return 0
	}

	res := durationReg.FindStringSubmatch(raw)
	if len(res) != 4 {
		return 0
	}

	hour, _ := strconv.Atoi(res[1])
	minute, _ := strconv.Atoi(res[2])
	second, _ := strconv.ParseFloat(res[3], 64)
	return time.Hour*time.Duration(hour) +
		time.Minute*time.Duration(minute) +
		time.Duration(float64(time.Second)*second)
}

// resolveLyrics 解析 FFmpeg 的 Lyrics 参数
func resolveLyrics(raw string) string {
	if !lyricsReg.MatchString(raw) {
		return ""
	}

	sb := strings.Builder{}

	results := lyricsReg.FindAllStringSubmatch(raw, -1)
	for i, result := range results {
		sb.WriteString(result[1])
		if i < len(results)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// resolveTrack 解析 FFmpeg 的 Track 参数
func resolveTrack(raw string) string {
	if !trackReg.MatchString(raw) {
		return ""
	}

	track := strings.TrimSpace(trackReg.FindStringSubmatch(raw)[1])
	if track == "" || strings.Contains(track, "/") {
		return track
	}

	// 尝试解析总轨道数
	if !trackTotalReg.MatchString(raw) {
		return track
	}
	trackTotal := strings.TrimSpace(trackTotalReg.FindStringSubmatch(raw)[1])
	if trackTotal == "" {
		return track
	}
	return fmt.Sprintf("%s/%s", track, trackTotal)
}

// resolveDisc 解析 FFmpeg 的 Disc 参数
func resolveDisc(raw string) string {
	if !discReg.MatchString(raw) {
		return ""
	}

	disc := strings.TrimSpace(discReg.FindStringSubmatch(raw)[1])
	if disc == "" || strings.Contains(disc, "/") {
		return disc
	}

	// 尝试解析总光盘数
	if !discTotalReg.MatchString(raw) {
		return disc
	}
	discTotal := strings.TrimSpace(discTotalReg.FindStringSubmatch(raw)[1])
	if discTotal == "" {
		return disc
	}
	return fmt.Sprintf("%s/%s", disc, discTotal)
}

// resolveTitle 解析 FFmpeg 的 title 参数
func resolveTitle(raw string) string {
	// 保留容器和音频流元数据，避免封面或字幕的标题、歌词参与回退。
	metadata := []string{raw}
	streams := streamReg.FindAllStringIndex(raw, -1)
	if len(streams) > 0 {
		metadata[0] = raw[:streams[0][0]]
		for i, loc := range streams {
			if !strings.Contains(raw[loc[0]:loc[1]], ": Audio:") {
				continue
			}
			end := len(raw)
			if i+1 < len(streams) {
				end = streams[i+1][0]
			}
			metadata = append(metadata, raw[loc[1]:end])
		}
	}

	for _, reg := range []*regexp.Regexp{titleReg, titleTiReg} {
		for _, section := range metadata {
			for _, match := range reg.FindAllStringSubmatch(section, -1) {
				if title := strings.TrimSpace(match[1]); title != "" {
					return title
				}
			}
		}
	}

	return ""
}
