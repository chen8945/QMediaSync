package music

import (
	"encoding/xml"
	"fmt"
	"os"

	"qmediasync/emby302/service/lib/ffmpeg"
)

// MusicNFO 音乐元数据
type MusicNFO struct {
	XMLName xml.Name `xml:"music"`
	Title   string   `xml:"title,omitempty"`
	Artist  string   `xml:"artist,omitempty"`
	Album   string   `xml:"album,omitempty"`
	Year    string   `xml:"year,omitempty"`
	Track   string   `xml:"track,omitempty"`
	Lyrics  string   `xml:"lyrics,omitempty"`
	Comment string   `xml:"comment,omitempty"`
}

// WriteNFO 写入音乐元数据到本地
func WriteNFO(filePath string, meta ffmpeg.Music) error {

	info := MusicNFO{
		Title:   meta.Title,
		Artist:  meta.Artist,
		Album:   meta.Album,
		Year:    meta.Date,
		Track:   meta.Track,
		Lyrics:  meta.Lyrics,
		Comment: meta.Comment,
	}

	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("无法创建文件: %w", err)
	}
	defer f.Close()

	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	return enc.Encode(info)
}
