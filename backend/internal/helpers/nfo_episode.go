package helpers

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

type TVShowEpisode struct {
	XMLName       xml.Name   `xml:"episodedetails"`
	Outline       string     `xml:"outline,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Tagline       string     `xml:"tagline,omitempty"`
	Title         string     `xml:"title,omitempty"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	SortTitle     string     `xml:"sorttitle,omitempty"`
	Premiered     string     `xml:"premiered,omitempty"`
	Releasedate   string     `xml:"releasedate,omitempty"`
	Year          int        `xml:"year,omitempty"`
	SeasonNumber  int        `xml:"seasonnumber,omitempty"`
	EpisodeNumber int        `xml:"episodenumber,omitempty"`
	Season        int        `xml:"season,omitempty"`
	Episode       int        `xml:"episode,omitempty"`
	DateAdded     string     `xml:"dateadded,omitempty"`
	Actor         []Actor    `xml:"actor,omitempty"`
	Director      []Director `xml:"director,omitempty"`
	// 评分信息
	Rating float64 `xml:"rating,omitempty"`
	Votes  int     `xml:"votes,omitempty"`
}

// ReadEpisodeNfo 解析集 NFO，兼容非 UTF-8 编码声明和数值标签格式异常
func ReadEpisodeNfo(r io.Reader) (*TVShowEpisode, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return unmarshalNfo[TVShowEpisode](b)
}

func WriteEpisodeNfo(m *TVShowEpisode, filename string) error {
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
