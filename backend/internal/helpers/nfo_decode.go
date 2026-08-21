package helpers

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html/charset"
)

// NFO 里常见的数值标签。第三方刮削工具可能写成 "120 min"、"8.6 分" 这类带单位的值，
// encoding/xml 解析这些标签时会让整份 NFO 解析失败，因此在解析失败后清洗数值再重试一次。
const nfoNumericTagNames = `runtime|year|top250|playcount|userrating|tmdbid|value|votes|position|total|bitrate|width|height|duration|durationinseconds|samplingrate|channels|season|episode|seasonnumber|episodenumber|displayseason|displayepisode|number`

// 匹配数值标签及其内容，RE2 不支持反向引用，开始和结束标签是否成对由替换回调判断
var nfoNumericTagPattern = regexp.MustCompile(`(?is)<(` + nfoNumericTagNames + `)((?:\s[^>]*)?)>([^<]*)</\s*(` + nfoNumericTagNames + `)\s*>`)

// NFO 数值标签中可用的数字部分
var nfoNumberPattern = regexp.MustCompile(`-?\d+(\.\d+)?`)

// decodeNfo 解析 NFO 内容，支持 GBK 等非 UTF-8 编码声明。
func decodeNfo(content []byte, target any) error {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.CharsetReader = charset.NewReaderLabel
	return decoder.Decode(target)
}

// unmarshalNfo 解析 NFO 内容；数值标签格式异常导致解析失败时，清洗数值后重试一次。
func unmarshalNfo[T any](content []byte) (*T, error) {
	target := new(T)
	err := decodeNfo(content, target)
	if err == nil {
		return target, nil
	}
	cleaned, changed := sanitizeNfoNumericTags(content)
	if !changed {
		return nil, err
	}
	// 使用新对象重试，避免第一次解析残留的切片元素重复累加
	retried := new(T)
	if retryErr := decodeNfo(cleaned, retried); retryErr != nil {
		return nil, err
	}
	AppLogger.Warnf("NFO 数值标签格式异常，已忽略异常数值后重新解析：%v", err)
	return retried, nil
}

// sanitizeNfoNumericTags 把数值标签的内容替换为其中的数字，取不到数字时清空标签内容。
// 返回清洗后的内容和是否发生过替换。
func sanitizeNfoNumericTags(content []byte) ([]byte, bool) {
	changed := false
	cleaned := nfoNumericTagPattern.ReplaceAllFunc(content, func(match []byte) []byte {
		groups := nfoNumericTagPattern.FindSubmatch(match)
		if groups == nil {
			return match
		}
		openTag, attrs, closeTag := string(groups[1]), string(groups[2]), string(groups[4])
		value := strings.TrimSpace(string(groups[3]))
		if !strings.EqualFold(openTag, closeTag) {
			return match
		}
		if value == "" || nfoNumberPattern.FindString(value) == value {
			return match
		}
		changed = true
		return []byte(fmt.Sprintf("<%s%s>%s</%s>", openTag, attrs, nfoNumberPattern.FindString(value), closeTag))
	})
	return cleaned, changed
}

// nfoRootElement 返回 NFO 的根节点名称
func nfoRootElement(content []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.CharsetReader = charset.NewReaderLabel
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("读取 NFO 根节点失败：%v", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local, nil
		}
	}
}

// ReadNfoAsMovie 按根节点分派解析 NFO，并统一转换为电影结构。
// 媒体类型为其他时 NFO 是唯一的信息来源，扫描阶段允许 movie.nfo、tvshow.nfo 和 season.nfo，
// 按根节点分派可以避免根节点不是 movie 时整份 NFO 解析失败。
func ReadNfoAsMovie(content []byte) (*Movie, error) {
	root, err := nfoRootElement(content)
	if err != nil {
		return nil, err
	}
	switch root {
	case "movie":
		return ReadMovieNfo(content)
	case "tvshow":
		show, err := unmarshalNfo[TVShow](content)
		if err != nil {
			return nil, err
		}
		return tvShowToMovie(show), nil
	case "season":
		season, err := unmarshalNfo[TVShowSeason](content)
		if err != nil {
			return nil, err
		}
		return seasonToMovie(season), nil
	case "episodedetails":
		episode, err := unmarshalNfo[TVShowEpisode](content)
		if err != nil {
			return nil, err
		}
		return episodeToMovie(episode), nil
	default:
		return nil, fmt.Errorf("不支持的 NFO 根节点：%s", root)
	}
}

// tvShowToMovie 把剧集 NFO 转换为电影结构，只保留识别和命名需要的字段
func tvShowToMovie(show *TVShow) *Movie {
	return &Movie{
		Title:         show.Title,
		OriginalTitle: show.OriginalTitle,
		SortTitle:     show.SortTitle,
		Outline:       show.Outline,
		Plot:          show.Plot,
		Tagline:       show.Tagline,
		Runtime:       show.Runtime,
		MPAA:          show.MPAA,
		Id:            show.Id,
		TmdbId:        show.TmdbId,
		ImdbId:        show.ImdbId,
		Uniqueid:      show.Uniqueid,
		Genre:         show.Genre,
		Director:      show.Director,
		Actor:         show.Actor,
		Premiered:     show.Premiered,
		Year:          show.Year,
		Code:          show.Code,
		Aired:         show.Aired,
		Studio:        show.Studio,
		DateAdded:     show.DateAdded,
	}
}

// seasonToMovie 把季 NFO 转换为电影结构，只保留识别和命名需要的字段
func seasonToMovie(season *TVShowSeason) *Movie {
	return &Movie{
		Title:         season.Title,
		OriginalTitle: season.OriginalTitle,
		Outline:       season.Outline,
		Plot:          season.Plot,
		Tagline:       season.Tagline,
		Premiered:     season.Premiered,
		ReleaseDate:   season.Releasedate,
		Year:          season.Year,
		DateAdded:     season.DateAdded,
	}
}

// episodeToMovie 把集 NFO 转换为电影结构，只保留识别和命名需要的字段
func episodeToMovie(episode *TVShowEpisode) *Movie {
	return &Movie{
		Title:         episode.Title,
		OriginalTitle: episode.OriginalTitle,
		SortTitle:     episode.SortTitle,
		Outline:       episode.Outline,
		Plot:          episode.Plot,
		Tagline:       episode.Tagline,
		Premiered:     episode.Premiered,
		ReleaseDate:   episode.Releasedate,
		Year:          episode.Year,
		Director:      episode.Director,
		Actor:         episode.Actor,
		DateAdded:     episode.DateAdded,
	}
}
