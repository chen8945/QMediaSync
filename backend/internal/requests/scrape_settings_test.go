package requests

import (
	"testing"

	"qmediasync/internal/models"
)

func TestScrapeSettingsRequestValidate(t *testing.T) {
	t.Run("TMDB 设置通过", func(t *testing.T) {
		req := TMDBSettingsRequest{
			TmdbURL:           "https://api.themoviedb.org",
			TmdbImageURL:      "https://image.tmdb.org",
			TmdbLanguage:      "zh-CN",
			TmdbImageLanguage: "en-US",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("TMDB URL 非法失败", func(t *testing.T) {
		req := TMDBSettingsRequest{TmdbURL: "ftp://example.com"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("TMDB 语言代码非法失败", func(t *testing.T) {
		req := TMDBSettingsRequest{TmdbLanguage: "zh-cn"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("AI 设置通过", func(t *testing.T) {
		req := AISettingsRequest{
			EnableAI:    models.AiActionAssist,
			AIBaseURL:   "https://api.example.com",
			AIModelName: "qwen",
			AITimeout:   120,
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("AI 超时小于 5 失败", func(t *testing.T) {
		req := AISettingsRequest{EnableAI: models.AiActionOff, AITimeout: 4}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestCategoryRequestValidate(t *testing.T) {
	t.Run("电影分类通过", func(t *testing.T) {
		req := MovieCategoryRequest{Name: "中文电影", LanguageArray: []string{"zh-CN"}, GenreIDArray: []int{28}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("电影分类语言非法失败", func(t *testing.T) {
		req := MovieCategoryRequest{Name: "中文电影", LanguageArray: []string{"zh-cn"}}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("电视剧分类通过", func(t *testing.T) {
		req := TVShowCategoryRequest{Name: "美剧", CountryArray: []string{"US"}, GenreIDArray: []int{18}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("电视剧国家代码非法失败", func(t *testing.T) {
		req := TVShowCategoryRequest{Name: "美剧", CountryArray: []string{"usa"}}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("分类名含路径穿越失败", func(t *testing.T) {
		names := []string{"../../outside", `..\..\outside`, "华语/电影", "..", ".", "   "}
		for _, name := range names {
			movie := MovieCategoryRequest{Name: name, LanguageArray: []string{"zh-CN"}, GenreIDArray: []int{28}}
			if err := movie.Validate(); err == nil {
				t.Errorf("电影分类名 %q 应该校验失败", name)
			}
			tvshow := TVShowCategoryRequest{Name: name, CountryArray: []string{"US"}, GenreIDArray: []int{18}}
			if err := tvshow.Validate(); err == nil {
				t.Errorf("电视剧分类名 %q 应该校验失败", name)
			}
		}
	})
}

func TestTmdbSearchRequestValidate(t *testing.T) {
	t.Run("按名称搜索通过", func(t *testing.T) {
		req := TMDBSearchRequest{Name: "Inception", Year: 2010, Type: models.MediaTypeMovie}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("按 TMDB ID 搜索通过", func(t *testing.T) {
		req := TMDBSearchRequest{TmdbID: 550, Type: models.MediaTypeTvShow}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("名称和 TMDB ID 都为空失败", func(t *testing.T) {
		req := TMDBSearchRequest{Type: models.MediaTypeMovie}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("年份超出范围失败", func(t *testing.T) {
		req := TMDBSearchRequest{Name: "Movie", Year: 1800, Type: models.MediaTypeMovie}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}
