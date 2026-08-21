package helpers

import (
	"io"
	"log"
	"testing"
)

func newNfoTestLogger(t *testing.T) {
	t.Helper()
	original := AppLogger
	t.Cleanup(func() {
		AppLogger = original
	})
	AppLogger = &QLogger{Logger: log.New(io.Discard, "", 0)}
}

func TestReadNfoAsMovie根节点分派(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedTitle string
		expectedYear  int
		expectError   bool
	}{
		{
			name:          "电影 NFO",
			content:       `<movie><title>某部影片</title><year>2024</year></movie>`,
			expectedTitle: "某部影片",
			expectedYear:  2024,
		},
		{
			name:          "剧集 NFO",
			content:       `<tvshow><title>某部剧集</title><year>2023</year></tvshow>`,
			expectedTitle: "某部剧集",
			expectedYear:  2023,
		},
		{
			name:          "季 NFO",
			content:       `<season><title>第一季</title><premiered>2022-01-01</premiered></season>`,
			expectedTitle: "第一季",
			expectedYear:  2022,
		},
		{
			name:          "集 NFO",
			content:       `<episodedetails><title>第一集</title><year>2021</year></episodedetails>`,
			expectedTitle: "第一集",
			expectedYear:  2021,
		},
		{
			name:          "带 XML 声明和 BOM",
			content:       "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<movie><title>某部影片</title></movie>",
			expectedTitle: "某部影片",
		},
		{
			name:        "不支持的根节点",
			content:     `<musicvideo><title>某个 MV</title></musicvideo>`,
			expectError: true,
		},
		{
			name:        "内容不是 XML",
			content:     `这不是 NFO`,
			expectError: true,
		},
	}

	newNfoTestLogger(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			movie, err := ReadNfoAsMovie([]byte(tt.content))
			if tt.expectError {
				if err == nil {
					t.Fatalf("期望解析失败，实际成功：%+v", movie)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if movie.MediaTitle() != tt.expectedTitle {
				t.Errorf("标题不符\n期望: %s\n实际: %s", tt.expectedTitle, movie.MediaTitle())
			}
			if movie.MediaYear() != tt.expectedYear {
				t.Errorf("年份不符\n期望: %d\n实际: %d", tt.expectedYear, movie.MediaYear())
			}
		})
	}
}

func TestReadNfoAsMovie数值标签格式异常(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		expectedRuntime int64
		expectedYear    int
	}{
		{
			name:            "运行时间带单位",
			content:         `<movie><title>某部影片</title><runtime>120 min</runtime><year>2024</year></movie>`,
			expectedRuntime: 120,
			expectedYear:    2024,
		},
		{
			name:            "评分带中文单位",
			content:         `<movie><title>某部影片</title><userrating>8.6 分</userrating><runtime>90</runtime></movie>`,
			expectedRuntime: 90,
		},
		{
			name:         "年份带中文单位",
			content:      `<movie><title>某部影片</title><year>2024 年</year></movie>`,
			expectedYear: 2024,
		},
		{
			name:            "数值标签为空",
			content:         `<movie><title>某部影片</title><runtime></runtime><year></year></movie>`,
			expectedRuntime: 0,
		},
		{
			name:            "数值标签没有数字",
			content:         `<movie><title>某部影片</title><runtime>未知</runtime></movie>`,
			expectedRuntime: 0,
		},
	}

	newNfoTestLogger(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			movie, err := ReadNfoAsMovie([]byte(tt.content))
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if movie.MediaTitle() != "某部影片" {
				t.Errorf("标题不符，实际：%s", movie.MediaTitle())
			}
			if movie.Runtime != tt.expectedRuntime {
				t.Errorf("运行时间不符\n期望: %d\n实际: %d", tt.expectedRuntime, movie.Runtime)
			}
			if movie.MediaYear() != tt.expectedYear {
				t.Errorf("年份不符\n期望: %d\n实际: %d", tt.expectedYear, movie.MediaYear())
			}
		})
	}
}

// 数值异常触发重试时使用新对象解析，切片字段不能重复累加
func TestReadNfoAsMovie重试不重复累加切片(t *testing.T) {
	newNfoTestLogger(t)
	content := `<movie>
  <title>某部影片</title>
  <runtime>120 min</runtime>
  <actor><name>演员甲</name></actor>
  <actor><name>演员乙</name></actor>
  <genre>剧情</genre>
</movie>`
	movie, err := ReadNfoAsMovie([]byte(content))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(movie.Actor) != 2 {
		t.Errorf("演员数量不符\n期望: 2\n实际: %d（%+v）", len(movie.Actor), movie.Actor)
	}
	if len(movie.Genre) != 1 {
		t.Errorf("流派数量不符\n期望: 1\n实际: %d（%+v）", len(movie.Genre), movie.Genre)
	}
}

func TestReadNfoAsMovieGBK编码(t *testing.T) {
	newNfoTestLogger(t)
	// 声明 GBK 编码，标题使用 GBK 字节序列（某部影片）
	content := []byte(`<?xml version="1.0" encoding="GBK"?><movie><title>`)
	content = append(content, 0xc4, 0xb3, 0xb2, 0xbf, 0xd3, 0xb0, 0xc6, 0xac)
	content = append(content, []byte(`</title><num>ABC-123</num></movie>`)...)
	movie, err := ReadNfoAsMovie(content)
	if err != nil {
		t.Fatalf("解析 GBK 编码的 NFO 失败：%v", err)
	}
	if movie.MediaTitle() != "某部影片" {
		t.Errorf("标题不符，实际：%s", movie.MediaTitle())
	}
	if movie.MediaNum() != "ABC-123" {
		t.Errorf("番号不符，实际：%s", movie.MediaNum())
	}
}

func TestMovieMediaNum(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "num 标签",
			content:  `<movie><title>ABC-123</title><num>ABC-123</num></movie>`,
			expected: "ABC-123",
		},
		{
			name:     "uniqueid 标签",
			content:  `<movie><title>ABC-123</title><uniqueid type="num">ABC-123</uniqueid></movie>`,
			expected: "ABC-123",
		},
		{
			name:     "code 标签",
			content:  `<movie><title>ABC-123</title><code>ABC-123</code></movie>`,
			expected: "ABC-123",
		},
		{
			name:     "id 标签",
			content:  `<movie><title>ABC-123</title><id>ABC-123</id></movie>`,
			expected: "ABC-123",
		},
		{
			name:     "id 是 IMDb ID 时不作为番号",
			content:  `<movie><title>某部影片</title><id>tt0816692</id></movie>`,
			expected: "",
		},
		{
			name:     "id 是纯数字时不作为番号",
			content:  `<movie><title>某部影片</title><id>157336</id></movie>`,
			expected: "",
		},
		{
			name:     "num 优先于 uniqueid",
			content:  `<movie><title>ABC-123</title><num>ABC-123</num><uniqueid type="num">XYZ-999</uniqueid></movie>`,
			expected: "ABC-123",
		},
		{
			name:     "没有任何番号字段",
			content:  `<movie><title>某部影片</title><year>2024</year></movie>`,
			expected: "",
		},
	}

	newNfoTestLogger(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			movie, err := ReadNfoAsMovie([]byte(tt.content))
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if result := movie.MediaNum(); result != tt.expected {
				t.Errorf("番号不符\n期望: %s\n实际: %s", tt.expected, result)
			}
		})
	}
}

func TestMovieMediaTitleAndYear(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedTitle string
		expectedYear  int
	}{
		{
			name:          "只有原始标题",
			content:       `<movie><originaltitle>Some Movie</originaltitle></movie>`,
			expectedTitle: "Some Movie",
		},
		{
			name:          "只有排序标题",
			content:       `<movie><sorttitle>Some Movie</sorttitle></movie>`,
			expectedTitle: "Some Movie",
		},
		{
			name:          "标题带空白",
			content:       `<movie><title>  某部影片  </title></movie>`,
			expectedTitle: "某部影片",
		},
		{
			name:          "年份来自首播日期",
			content:       `<movie><title>某部影片</title><premiered>2024-05-01</premiered></movie>`,
			expectedTitle: "某部影片",
			expectedYear:  2024,
		},
		{
			name:          "年份来自发行日期",
			content:       `<movie><title>某部影片</title><releasedate>2023-05-01</releasedate></movie>`,
			expectedTitle: "某部影片",
			expectedYear:  2023,
		},
		{
			name:          "日期格式异常时年份为零",
			content:       `<movie><title>某部影片</title><premiered>24</premiered></movie>`,
			expectedTitle: "某部影片",
			expectedYear:  0,
		},
	}

	newNfoTestLogger(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			movie, err := ReadNfoAsMovie([]byte(tt.content))
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if movie.MediaTitle() != tt.expectedTitle {
				t.Errorf("标题不符\n期望: %s\n实际: %s", tt.expectedTitle, movie.MediaTitle())
			}
			if movie.MediaYear() != tt.expectedYear {
				t.Errorf("年份不符\n期望: %d\n实际: %d", tt.expectedYear, movie.MediaYear())
			}
		})
	}
}

func TestLooksLikeMediaNum(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{name: "番号", value: "ABC-123", expected: true},
		{name: "无分隔符番号", value: "ABC123", expected: true},
		{name: "TT 开头的番号", value: "TT-123", expected: true},
		{name: "IMDb ID", value: "tt0816692", expected: false},
		{name: "大写 IMDb ID", value: "TT0816692", expected: false},
		{name: "纯数字", value: "157336", expected: false},
		{name: "纯字母", value: "unknown", expected: false},
		{name: "空值", value: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := looksLikeMediaNum(tt.value); result != tt.expected {
				t.Errorf("判断 '%s' 失败\n期望: %v\n实际: %v", tt.value, tt.expected, result)
			}
		})
	}
}
