package ffmpeg

import (
	"testing"
	"time"
)

func TestParseMusicMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   Music
	}{
		{
			name: "complete_metadata",
			output: `Input #0, flac, from 'song.flac':
  Metadata:
    album_artist : Various Artists
    artist       : Singer
    album        : Collection
    comment      : recorded : live
    date         : 2024
    tdor         : 1999
    title        : Main title
    track        : 03
    tracktotal   : 12
    disc         : 1
    disctotal    : 2
    genre        : Rock
    lyrics       : [ti:Lyric title]
                 : [00:01.00]First line
                 : [00:02.00]Second line
  Duration: 00:03:04.50, start: 0.000000, bitrate: 1000 kb/s
  Stream #0:0: Audio: flac
    Metadata:
      title      : Stream title
`,
			want: Music{
				Info:        Info{Duration: 184500 * time.Millisecond},
				Album:       "Collection",
				Artist:      "Singer",
				AlbumArtist: "Various Artists",
				Comment:     "recorded : live",
				Date:        "2024",
				Lyrics:      "[ti:Lyric title]\n[00:01.00]First line\n[00:02.00]Second line",
				Title:       "Main title",
				Track:       "03/12",
				Disc:        "1/2",
				Genre:       "Rock",
			},
		},
		{
			name:   "album_artist_without_artist",
			output: "    album_artist : Various Artists\n",
			want:   Music{AlbumArtist: "Various Artists"},
		},
		{
			name: "empty_tags_do_not_consume_next_line",
			output: `    album :
    artist :
    album_artist :
    comment :
    date :
    tdor :
    title :
    track :
    tracktotal :
    disc :
    disctotal :
    genre :
    other : Next value
`,
		},
		{
			name:   "empty_artist_before_title",
			output: "    artist : \t\n    title : Song\n",
			want:   Music{Title: "Song"},
		},
		{
			name:   "case_tabs_and_crlf",
			output: "\tALBUM_ARTIST\t:\t合辑艺人 \t\r\n\tArTiSt : 歌手 \t\r\n\tAlBuM : 专辑\r\n\tTiTlE\t:\t标题\t\r\n\tDaTe : 2025\r\n",
			want: Music{
				AlbumArtist: "合辑艺人",
				Artist:      "歌手",
				Album:       "专辑",
				Title:       "标题",
				Date:        "2025",
			},
		},
		{
			name:   "date_preferred_regardless_of_tag_order",
			output: "    tdor : 1999\n    date : 2024\n",
			want:   Music{Date: "2024"},
		},
		{
			name:   "tdor_when_date_missing",
			output: "    tdor : 1999\n",
			want:   Music{Date: "1999"},
		},
		{
			name:   "tdor_when_date_empty",
			output: "    date : \t\r\n    tdor : 1999\r\n",
			want:   Music{Date: "1999"},
		},
		{
			name:   "title_falls_back_to_lyrics",
			output: "    title :\n    lyrics : [ti:Lyric title]\n",
			want:   Music{Title: "Lyric title", Lyrics: "[ti:Lyric title]"},
		},
		{
			name:   "inline_track_and_disc_totals",
			output: "    track : 2/10\n    tracktotal : 99\n    disc : 1/3\n    disctotal : 99\n",
			want:   Music{Track: "2/10", Disc: "1/3"},
		},
		{
			name:   "empty_totals_keep_numbers",
			output: "    track : 2\n    tracktotal : \t\n    disc : 1\n    disctotal : \t\n    genre : Pop\n",
			want:   Music{Track: "2", Disc: "1", Genre: "Pop"},
		},
		{
			name:   "empty_numbers_ignore_totals",
			output: "    track :\n    tracktotal : 12\n    disc :\n    disctotal : 2\n",
		},
		{
			name:   "keys_cannot_span_lines",
			output: "    artist\n    : Wrong artist\n    album\n    : Wrong album\n",
		},
		{
			name:   "reject_other_key_prefixes",
			output: "    original_artist : Other artist\n    original_date : 1990\n    subtitle : Other title\n    my_album : Other album\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseMusicMetadata(tc.output); got != tc.want {
				t.Fatalf("音乐元数据 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

func TestResolveTitleStreamMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "ogg_audio_title_after_duration",
			output: `Input #0, ogg, from 'song.ogg':
  Duration: 00:00:00.10, start: 0.000000, bitrate: 305 kb/s
  Stream #0:0: Audio: vorbis, 44100 Hz, mono, fltp, 80 kb/s
    Metadata:
      title           : Ogg song
      artist          : Singer
`,
			want: "Ogg song",
		},
		{
			name: "skip_empty_titles_and_non_audio_streams",
			output: `  Metadata:
    title :
  Duration: 00:03:00.00, start: 0.000000
  Stream #0:0: Video: mjpeg (attached pic)
    Metadata:
      title : Cover
  Stream #0:1(eng): Audio: vorbis
    Metadata:
      title :
  Stream #0:2(chi): Subtitle: subrip
    Metadata:
      title : Subtitles
  Stream #0:3(eng): Audio: vorbis
    Metadata:
      title :
      title : First audio song
  Stream #0:4: Audio: vorbis
    Metadata:
      title : Second audio song
`,
			want: "First audio song",
		},
		{
			name: "audio_title_before_container_lyrics",
			output: `  Metadata:
    lyrics : [ti:Container lyric title]
  Duration: 00:03:00.00, start: 0.000000
  Stream #0:0: Audio: vorbis
    Metadata:
      title : Audio song
`,
			want: "Audio song",
		},
		{
			name: "audio_lyrics_fallback_skips_empty_ti",
			output: `  Duration: 00:03:00.00, start: 0.000000
  Stream #0:0: Video: mjpeg (attached pic)
    Metadata:
      lyrics : [ti:Cover lyric title]
  Stream #0:1: Audio: vorbis
    Metadata:
      title :
      lyrics : [ti:]
             : [ti:Audio lyric title]
  Stream #0:2: Subtitle: subrip
    Metadata:
      lyrics : [ti:Subtitle lyric title]
`,
			want: "Audio lyric title",
		},
		{
			name: "container_lyrics_before_audio_lyrics",
			output: `  Metadata:
    lyrics : [ti:Container lyric title]
  Duration: 00:03:00.00, start: 0.000000
  Stream #0:0: Audio: vorbis
    Metadata:
      title :
      lyrics : [ti:Audio lyric title]
`,
			want: "Container lyric title",
		},
		{
			name: "container_title_after_empty_title",
			output: `  Metadata:
    title :
    title : Container song
  Stream #0:0: Audio: vorbis
    Metadata:
      title : Audio song
`,
			want: "Container song",
		},
		{
			name: "non_audio_titles_and_lyrics_do_not_supply_song_title",
			output: `  Duration: 00:03:00.00, start: 0.000000
  Stream #0:0: Video: mjpeg (attached pic)
    Metadata:
      title : Cover
      lyrics : [ti:Cover lyric title]
  Stream #0:1: Audio: vorbis
    Metadata:
      artist : Singer
  Stream #0:2: Subtitle: subrip
    Metadata:
      title : Subtitles
      lyrics : [ti:Subtitle lyric title]
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveTitle(tc.output); got != tc.want {
				t.Fatalf("歌曲标题 = %q，期望 %q", got, tc.want)
			}
		})
	}
}
