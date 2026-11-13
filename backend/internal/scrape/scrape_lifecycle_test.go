package scrape

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/tmdb"
)

type cancelIdentify struct {
	ctx     context.Context
	entered chan struct{}
}

func TestCompletedScrapeCancelsOwnedContext(t *testing.T) {
	setupRenameTemplateTestDB(t)
	root := t.TempDir()
	path := &models.ScrapePath{
		SourceType: models.SourceTypeLocal, MediaType: models.MediaTypeMovie,
		SourcePath: root, SourcePathId: root, DestPath: root, DestPathId: root,
		ScrapeType: models.ScrapeTypeOnlyRename, MaxThreads: 1,
	}
	if err := db.Db.Create(path).Error; err != nil {
		t.Fatal(err)
	}
	scraper := NewScrape(path)
	defer scraper.ctxCancel()
	if !scraper.Start() {
		t.Fatal("empty local scrape should finish successfully")
	}
	select {
	case <-scraper.ctx.Done():
	default:
		t.Fatal("successful scrape retained its scanner cancellation waiter")
	}
}

func TestMovieReleaseDateFailureAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			setupRenameTemplateTestDB(t)
			oldLogger := helpers.TMDBLog
			helpers.TMDBLog = helpers.AppLogger
			defer func() { helpers.TMDBLog = oldLogger }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/release_dates"):
					if canceled {
						cancel()
						<-r.Context().Done()
						return
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				case strings.HasSuffix(r.URL.Path, "/credits"), strings.HasSuffix(r.URL.Path, "/images"):
					_, _ = w.Write([]byte(`{}`))
				default:
					_, _ = w.Write([]byte(`{"id":1,"title":"Captured movie","release_date":"2024-01-01"}`))
				}
			}))
			defer server.Close()
			client, err := tmdb.NewClient("key", "", server.URL, "en-US", "")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			impl := &movieScrapeImpl{ScrapeBase: ScrapeBase{ctx: ctx, tmdbClient: client}}
			file := &models.ScrapeMediaFile{MediaType: models.MediaTypeMovie, TmdbId: 1}
			err = impl.ScrapeMovieMedia(file)
			if canceled {
				if !errors.Is(err, context.Canceled) || file.Media != nil {
					t.Fatalf("canceled metadata stage continued: %v, %+v", err, file.Media)
				}
			} else if err != nil || file.Media == nil || file.Media.Name != "Captured movie" {
				t.Fatalf("optional release-date failure discarded metadata: %v, %+v", err, file.Media)
			}
		})
	}
}

func (i cancelIdentify) Identify(*models.ScrapeMediaFile) error {
	close(i.entered)
	<-i.ctx.Done()
	return i.ctx.Err()
}

func TestCanceledScrapeDrainsPendingWorkBeforeReturning(t *testing.T) {
	for _, mediaType := range []models.MediaType{models.MediaTypeMovie, models.MediaTypeTvShow} {
		t.Run(string(mediaType), func(t *testing.T) {
			setupRenameTemplateTestDB(t)
			path := &models.ScrapePath{MediaType: mediaType, SourceType: models.SourceTypeLocal, MaxThreads: 1}
			if err := db.Db.Create(path).Error; err != nil {
				t.Fatal(err)
			}
			for index := range 5 {
				file := &models.ScrapeMediaFile{ScrapePathId: path.ID, MediaType: mediaType, Status: models.ScrapeMediaStatusScanned, TvshowPathId: fmt.Sprintf("show-%d", index), VideoFilename: fmt.Sprintf("video-%d.mkv", index)}
				if err := db.Db.Create(file).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			var impl scrapeImpl
			if mediaType == models.MediaTypeMovie {
				movie, err := NewMovieScrapeImpl(path, ctx, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				movie.identifyImpl = cancelIdentify{ctx, entered}
				impl = movie
			} else {
				show, err := NewTvShowScrapeImpl(path, ctx, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				show.identifyImpl = cancelIdentify{ctx, entered}
				impl = show
			}
			defer impl.Close()
			result := make(chan error, 1)
			go func() { result <- impl.Start() }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("scrape did not begin")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled scrape: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled queue retained pending work or workers")
			}
		})
	}
}
