package helpers

import (
	"encoding/xml"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// <fileinfo>
//
//	<streamdetails>
//	  <video>
//	    <codec>hevc</codec>
//	    <micodec>hevc</micodec>
//	    <bitrate>5568397</bitrate>
//	    <width>3840</width>
//	    <height>1680</height>
//	    <aspect>16:7</aspect>
//	    <aspectratio>16:7</aspectratio>
//	    <framerate>25</framerate>
//	    <scantype>progressive</scantype>
//	    <default>True</default>
//	    <forced>False</forced>
//	    <duration>59</duration>
//	    <durationinseconds>3586</durationinseconds>
//	  </video>
//	  <audio>
//	    <codec>aac</codec>
//	    <micodec>aac</micodec>
//	    <bitrate>192000</bitrate>
//	    <language>kor</language>
//	    <scantype>progressive</scantype>
//	    <channels>2</channels>
//	    <samplingrate>48000</samplingrate>
//	    <default>True</default>
//	    <forced>False</forced>
//	  </audio>
//	  <subtitle>
//	    <codec>subrip</codec>
//	    <micodec>subrip</micodec>
//	    <language>chi</language>
//	    <scantype>progressive</scantype>
//	    <default>True</default>
//	    <forced>False</forced>
//	  </subtitle>
//	  <subtitle>
//	    <codec>subrip</codec>
//	    <micodec>subrip</micodec>
//	    <language>chi</language>
//	    <scantype>progressive</scantype>
//	    <default>False</default>
//	    <forced>False</forced>
//	  </subtitle>
//	  <subtitle>
//	    <codec>subrip</codec>
//	    <micodec>subrip</micodec>
//	    <language>eng</language>
//	    <scantype>progressive</scantype>
//	    <default>False</default>
//	    <forced>False</forced>
//	  </subtitle>
//	</streamdetails>
//
// </fileinfo>
type Movie struct {
	XMLName       xml.Name `xml:"movie"`
	Title         string   `xml:"title,omitempty"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	SortTitle     string   `xml:"sorttitle,omitempty"`
	ReleaseDate   string   `xml:"releasedate,omitempty"`
	Ratings       struct {
		Rating []Rating `xml:"rating,omitempty"`
	} `xml:"ratings"`
	UserRating float64    `xml:"userrating,omitempty"`
	Top250     int64      `xml:"top250,omitempty"`
	Outline    string     `xml:"outline,omitempty"`
	Plot       string     `xml:"plot,omitempty"`
	Tagline    string     `xml:"tagline,omitempty"`
	Runtime    int64      `xml:"runtime,omitempty"`
	Thumb      []Thumb    `xml:"thumb,omitempty"`
	Fanart     *Fanart    `xml:"fanart,omitempty"`
	MPAA       string     `xml:"mpaa,omitempty"`
	Playcount  int64      `xml:"playcount,omitempty"`
	Lastplayed string     `xml:"lastplayed,omitempty"`
	Id         string     `xml:"id,omitempty"`
	Num        string     `xml:"num,omitempty"`
	TmdbId     int64      `xml:"tmdbid,omitempty"`
	ImdbId     string     `xml:"imdbid,omitempty"`
	Uniqueid   []UniqueId `xml:"uniqueid,omitempty"`
	Genre      []string   `xml:"genre,omitempty"`
	Tag        []string   `xml:"tag,omitempty"`
	Set        struct {
		Name     string `xml:"name,omitempty"`
		Overview string `xml:"overview,omitempty"`
	} `xml:"set,omitempty"`
	Country   string     `xml:"country,omitempty"`
	Credits   string     `xml:"credits,omitempty"`
	Director  []Director `xml:"director,omitempty"`
	Premiered string     `xml:"premiered,omitempty"`
	Year      int        `xml:"year,omitempty"`
	Status    string     `xml:"status,omitempty"`
	Code      string     `xml:"code,omitempty"`
	Aired     string     `xml:"aired,omitempty"`
	Studio    string     `xml:"studio,omitempty"`
	Trailer   string     `xml:"trailer,omitempty"`
	FileInfo  struct {
		StreamDetails struct {
			Video    []StreamVideo    `xml:"video,omitempty"`
			Audio    []StreamAudio    `xml:"audio,omitempty"`
			Subtitle []StreamSubtitle `xml:"subtitle,omitempty"`
		} `xml:"streamdetails,omitempty"`
	} `xml:"fileinfo,omitempty"`
	Actor  []Actor `xml:"actor,omitempty"`
	Resume struct {
		Position float64 `xml:"position,omitempty"`
		Total    float64 `xml:"total,omitempty"`
	} `xml:"resume,omitempty"`
	DateAdded string `xml:"dateadded,omitempty"`
}

type Rating struct {
	Value   float64 `xml:"value,omitempty"`
	Votes   int64   `xml:"votes,omitempty"`
	Name    string  `xml:"name,attr,omitempty"`
	Max     int64   `xml:"max,attr,omitempty"`
	Default bool    `xml:"default,attr,omitempty"`
}

type Fanart struct {
	Thumb []Thumb `xml:"thumb,omitempty"`
}

type Thumb struct {
	Spoof   string `xml:"spoof,omitempty,attr"`
	Aspect  string `xml:"aspect,omitempty,attr"`
	Cache   string `xml:"cache,omitempty,attr"`
	Preview string `xml:"preview,omitempty,attr"`
	Colors  string `xml:"colors,omitempty,attr"`
	Link    string `xml:",chardata"`
}

type UniqueId struct {
	Type    string `xml:"type,omitempty,attr"`
	Default bool   `xml:"default,omitempty,attr"`
	Id      string `xml:",chardata"`
}

type Director struct {
	TmdbId int64  `xml:"tmdbid,omitempty,attr"`
	Name   string `xml:",chardata"`
}

// 视频流，包括编码，宽高，时长等
type StreamVideo struct {
	Codec             string `xml:"codec,omitempty"`
	Micodec           string `xml:"micodec,omitempty"`
	Bitrate           int64  `xml:"bitrate,omitempty"`
	Aspect            string `xml:"aspect,omitempty"`
	Width             int64  `xml:"width,omitempty"`
	Height            int64  `xml:"height,omitempty"`
	AspectRatio       string `xml:"aspectratio,omitempty"`
	FrameRate         string `xml:"framerate,omitempty"`
	ScanType          string `xml:"scantype,omitempty"`
	Duration          int64  `xml:"duration,omitempty"`
	DurationInSeconds int64  `xml:"durationinseconds,omitempty"`
	StereoMode        string `xml:"stereomode,omitempty"`
	Forced            bool   `xml:"forced,omitempty"`
	Default           bool   `xml:"default,omitempty"`
}

// 音频流，可能有多个，包括编码，语言，通道数等
type StreamAudio struct {
	Codec        string `xml:"codec,omitempty"`
	Micodec      string `xml:"micodec,omitempty"`
	Bitrate      int64  `xml:"bitrate,omitempty"`
	ScanType     string `xml:"scantype,omitempty"`
	SamplingRate int64  `xml:"samplingrate,omitempty"`
	Language     string `xml:"language,omitempty"`
	Channels     int64  `xml:"channels,omitempty"`
	Default      bool   `xml:"default,omitempty"`
	Forced       bool   `xml:"forced,omitempty"`
}

// 字母幕流，可能有多个，包括语言等
type StreamSubtitle struct {
	Codec    string `xml:"codec,omitempty"`
	Micodec  string `xml:"micodec,omitempty"`
	Language string `xml:"language,omitempty"`
	ScanType string `xml:"scantype,omitempty"`
	Default  bool   `xml:"default,omitempty"`
	Forced   bool   `xml:"forced,omitempty"`
}

type Actor struct {
	Name    string `xml:"name,omitempty"`
	Role    string `xml:"role,omitempty"`
	Order   int64  `xml:"order,omitempty"`
	Thumb   string `xml:"thumb,omitempty"`
	TmdbId  int64  `xml:"tmdbid,omitempty"`
	Profile string `xml:"profile,omitempty"`
}

// IMDb ID 形式：tt 开头加数字，用于区分 id、code 里的番号和 IMDb ID
var imdbIDPattern = regexp.MustCompile(`(?i)^tt\d+$`)

// ReadMovieNfo 解析电影 NFO，兼容非 UTF-8 编码声明和数值标签格式异常
func ReadMovieNfo(b []byte) (*Movie, error) {
	return unmarshalNfo[Movie](b)
}

// MediaTitle 返回 NFO 中可用的标题，优先 title，其次 originaltitle、sorttitle
func (m *Movie) MediaTitle() string {
	for _, title := range []string{m.Title, m.OriginalTitle, m.SortTitle} {
		if trimmed := strings.TrimSpace(title); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// MediaYear 返回 NFO 中可用的年份，优先 year，其次从 premiered、releasedate、aired 中解析
func (m *Movie) MediaYear() int {
	if m.Year != 0 {
		return m.Year
	}
	for _, date := range []string{m.Premiered, m.ReleaseDate, m.Aired} {
		if year := ParseYearFromDate(strings.TrimSpace(date)); year != 0 {
			return year
		}
	}
	return 0
}

// MediaNum 返回 NFO 中可用的番号，优先 num，其次 uniqueid[type=num]，最后 code 和 id。
// code 和 id 也被用于存放制作编号、IMDb ID 或 TMDB ID，因此只接受番号形式的值。
func (m *Movie) MediaNum() string {
	if num := strings.TrimSpace(m.Num); num != "" {
		return num
	}
	for _, uid := range m.Uniqueid {
		if !strings.EqualFold(uid.Type, "num") {
			continue
		}
		if num := strings.TrimSpace(uid.Id); num != "" {
			return num
		}
	}
	for _, value := range []string{m.Code, m.Id} {
		if trimmed := strings.TrimSpace(value); looksLikeMediaNum(trimmed) {
			return trimmed
		}
	}
	return ""
}

// looksLikeMediaNum 判断值是否为番号形式：同时包含字母和数字，且不是 IMDb ID
func looksLikeMediaNum(value string) bool {
	if value == "" || imdbIDPattern.MatchString(value) {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

func WriteMovieNfo(m *Movie, filename string) error {
	xmlHeader := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n")

	data, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 XML 失败：%v", err)
	}

	content := append(xmlHeader, data...)
	strOutput := string(content)
	strOutput = strings.Replace(strOutput, "&lt;![CDATA[", "<![CDATA[", -1)
	strOutput = strings.Replace(strOutput, "]]&gt;", "]]>", -1)
	err = os.WriteFile(filename, []byte(strOutput), 0766)
	if err != nil {
		return fmt.Errorf("写入文件失败：%v", err)
	}

	return nil
}
