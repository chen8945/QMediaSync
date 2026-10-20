package syncstrm

import (
	"os"
	"path/filepath"
	"strings"
)

// collectMissingLocalVideos 在删除任何子文件前保留缺失视频的名称，避免元数据处理依赖遍历顺序。
func (s *SyncStrm) collectMissingLocalVideos(dir string, missing map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".strm" {
			continue
		}
		local := filepath.ToSlash(filepath.Join(dir, entry.Name()))
		if !s.memSyncCache.ExistsByLocalPath(local) {
			missing[local] = struct{}{}
		}
	}
	return nil
}

func (s *SyncStrm) missingMetadataVideoOwner(local string, missing map[string]struct{}) string {
	ext := strings.ToLower(filepath.Ext(local))
	image := false
	switch ext {
	case ".nfo":
	case ".jpg", ".jpeg", ".png", ".webp", ".tbn", ".svg":
		image = true
	default:
		return ""
	}
	stem := strings.TrimSuffix(local, filepath.Ext(local))
	base := strings.ToLower(filepath.Base(stem))
	if ext == ".nfo" && (base == "movie" || base == "tvshow") {
		return ""
	}
	if image {
		switch base {
		case "poster", "folder", "fanart", "banner", "landscape", "thumb", "clearlogo", "clearart", "logo",
			"backdrop", "background", "4kbackground", "disc", "discart", "disccart", "keyart", "square", "squareart":
			return ""
		}
	}
	if season, found := strings.CutPrefix(base, "season"); found {
		if image {
			season, _, _ = strings.Cut(season, "-")
		}
		numberedSeason := season != "" && strings.Trim(season, "0123456789") == ""
		if base == "season" || numberedSeason || image && strings.HasPrefix(base, "season-specials-") {
			return ""
		}
	}
	owner := func(stem string) (string, bool) {
		video := stem + ".strm"
		if s.memSyncCache.ExistsByLocalPath(video) {
			return "", true
		}
		if _, exists := missing[video]; exists {
			return video, true
		}
		return "", false
	}
	if video, matched := owner(stem); matched {
		return video
	}
	if image {
		for _, suffix := range []string{
			"-poster", "-fanart", "-thumb", "-banner", "-landscape", "-clearlogo", "-clearart", "-disc",
			"-square", "-logo", "-background", "-4kbackground", "-backdrop",
		} {
			if strings.HasSuffix(stem, suffix) {
				if video, matched := owner(strings.TrimSuffix(stem, suffix)); matched {
					return video
				}
			}
		}
	}
	return ""
}
