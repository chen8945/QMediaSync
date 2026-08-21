package helpers

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

type TVShow struct {
	XMLName       xml.Name `xml:"tvshow"`
	Title         string   `xml:"title,omitempty"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	SortTitle     string   `xml:"sorttitle,omitempty"`
	ShowTitle     string   `xml:"showtitle,omitempty"`
	Ratings       struct {
		Rating []Rating `xml:"rating,omitempty"`
	} `xml:"ratings"`
	UserRating     float64    `xml:"userrating,omitempty"`
	Top250         int64      `xml:"top250,omitempty"`
	Season         int64      `xml:"season,omitempty"`
	Episode        int64      `xml:"episode,omitempty"`
	DisplaySeason  int64      `xml:"displayseason,omitempty"`
	DisplayEpisode int64      `xml:"displayepisode,omitempty"`
	Outline        string     `xml:"outline,omitempty"`
	Plot           string     `xml:"plot,omitempty"`
	Tagline        string     `xml:"tagline,omitempty"`
	Runtime        int64      `xml:"runtime,omitempty"`
	Thumb          []Thumb    `xml:"thumb,omitempty"`
	Fanart         *Fanart    `xml:"fanart,omitempty"`
	MPAA           string     `xml:"mpaa,omitempty"`
	Playcount      int64      `xml:"playcount,omitempty"`
	Lastplayed     string     `xml:"lastplayed,omitempty"`
	Id             string     `xml:"id,omitempty"`
	TmdbId         int64      `xml:"tmdbid,omitempty"`
	ImdbId         string     `xml:"imdbid,omitempty"`
	Uniqueid       []UniqueId `xml:"uniqueid,omitempty"`
	Genre          []string   `xml:"genre,omitempty"`
	Premiered      string     `xml:"premiered,omitempty"`
	Year           int        `xml:"year,omitempty"`
	Status         string     `xml:"status,omitempty"`
	Code           string     `xml:"code,omitempty"`
	Aired          string     `xml:"aired,omitempty"`
	Studio         string     `xml:"studio,omitempty"`
	Actor          []Actor    `xml:"actor,omitempty"`
	Director       []Director `xml:"director,omitempty"`
	NamedSeason    struct {
		Number int64  `xml:"number,omitempty,attr"`
		Name   string `xml:",chardata"`
	} `xml:"namedseason,omitempty"`
	Resume struct {
		Position float64 `xml:"position,omitempty"`
		Total    float64 `xml:"total,omitempty"`
	} `xml:"resume,omitempty"`
	DateAdded string `xml:"dateadded,omitempty"`
}

// ReadTVShowNfo 解析剧集 NFO，兼容非 UTF-8 编码声明和数值标签格式异常
func ReadTVShowNfo(r io.Reader) (*TVShow, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return unmarshalNfo[TVShow](b)
}

func WriteTVShowNfo(m *TVShow, filename string) error {
	xmlHeader := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n")
	data, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	content := append(xmlHeader, data...)
	// 将字符串中的实体编码替换回原内容
	strOutput := string(content)
	strOutput = strings.Replace(strOutput, "&lt;![CDATA[", "<![CDATA[", -1)
	strOutput = strings.Replace(strOutput, "]]&gt;", "]]>", -1)
	err = os.WriteFile(filename, []byte(strOutput), 0766)
	if err != nil {
		return fmt.Errorf("写入文件失败：%v", err)
	}

	return nil
}
