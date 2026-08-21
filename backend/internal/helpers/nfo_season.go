package helpers

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

type TVShowSeason struct {
	XMLName       xml.Name `xml:"season"`
	Outline       string   `xml:"outline,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	Tagline       string   `xml:"tagline,omitempty"`
	Title         string   `xml:"title,omitempty"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	Premiered     string   `xml:"premiered,omitempty"`
	Releasedate   string   `xml:"releasedate,omitempty"`
	Year          int      `xml:"year,omitempty"`
	SeasonNumber  int      `xml:"seasonnumber,omitempty"`
	DateAdded     string   `xml:"dateadded,omitempty"`
}

// ReadSeasonNfo 解析季 NFO，兼容非 UTF-8 编码声明和数值标签格式异常
func ReadSeasonNfo(r io.Reader) (*TVShowSeason, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return unmarshalNfo[TVShowSeason](b)
}

func WriteSeasonNfo(m *TVShowSeason, filename string) error {
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
