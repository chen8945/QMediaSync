package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coocood/freecache"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

type externalTestTransport func(*http.Request) (*http.Response, error)

func (f externalTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type externalTestBody struct{ reads, closes int }

func (b *externalTestBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("video body must not be read")
}

func (b *externalTestBody) Close() error { b.closes++; return nil }

func setupExternalLinkTest(t *testing.T) {
	t.Helper()
	previousCache, previousPreloader := db.Cache, DefaultPreloader
	db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
	DefaultPreloader = NewPreloader()
	preloadLogger(t, io.Discard, helpers.LogLevelDebug)
	t.Cleanup(func() { db.Cache, DefaultPreloader = previousCache, previousPreloader })
}

func externalTestCandidate(t *testing.T, raw, ua string, transport http.RoundTripper) PreloadCandidate {
	t.Helper()
	identity, run, ok := NewExternalPreload(raw, ua, transport)
	if !ok {
		t.Fatalf("external preload rejected %q", raw)
	}
	candidate := preloadCandidate(identity, PreloadNext, run)
	candidate.Scope, candidate.Account = identity, ExternalPreloadAccount(raw)
	return candidate
}

func TestValidExternalURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"docker_without_qms_fields", "http://moviepilot:3000/plugin/redirect?opaque=third-party", true},
		{"encoded_path_and_repeated_query", "https://openlist.invalid/d/%E7%89%87%E5%90%8D%2Fmovie?key=a%2fb+z&x=2&x=1", true},
		{"ipv6", "http://[fd00::1]:5244/video", true},
		{"empty", "", false},
		{"relative", "/video", false},
		{"no_host", "https:///video", false},
		{"non_http", "ftp://host/video", false},
		{"userinfo", "https://user:password@host/video", false},
		{"opaque", "http:host/video", false},
		{"fragment", "https://host/video#part", false},
		{"newline", "https://host/video\n", false},
		{"delete_control", "https://host/\x7fvideo", false},
		{"too_long", "https://host/" + strings.Repeat("x", 16<<10), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidExternalURL(tc.raw); got != tc.want {
				t.Fatalf("ValidExternalURL(%q) = %t, want %t", tc.raw, got, tc.want)
			}
		})
	}
	for _, ua := range []string{"", " ", "Player\r\nCookie: secret", strings.Repeat("x", 4097)} {
		if _, _, ok := NewExternalPreload("http://moviepilot/video", ua, nil); ok {
			t.Fatalf("invalid UA accepted: %q", ua)
		}
	}
}

func TestExternalPreloadAccountUsesOriginWithoutLocalAccount(t *testing.T) {
	want := PreloadAccount{Provider: "http", UserID: "http://moviepilot:80"}
	for _, raw := range []string{"http://MoviePilot/a?x=1", "http://moviepilot:80/b?x=2", "http://moviepilot:080/b?x=2"} {
		if got := ExternalPreloadAccount(raw); got != want {
			t.Fatalf("account = %+v, want %+v", got, want)
		}
	}
	if ExternalPreloadAccount("https://moviepilot:00443/a") != ExternalPreloadAccount("https://moviepilot/a") {
		t.Fatal("leading zeroes must not split the same HTTPS origin")
	}
	for _, raw := range []string{"https://moviepilot/a", "http://moviepilot:3000/a", "http://other/a"} {
		if got := ExternalPreloadAccount(raw); got == want || got.AccountID != 0 {
			t.Fatalf("origin was merged or assigned a local account: %+v", got)
		}
	}
	if got := ExternalPreloadAccount("ftp://moviepilot/a"); got != (PreloadAccount{}) {
		t.Fatalf("invalid source acquired an account: %+v", got)
	}
}

func TestResolveExternalLinkPreservesRedirectsAndFiltersHeaders(t *testing.T) {
	setupExternalLinkTest(t)
	urls := []string{
		"http://moviepilot:3000/plugin/%E7%89%87%E5%90%8D%2Fstart?key=a%2fb+z&x=2&x=1",
		"http://moviepilot:3000/video%2Ftwo?x=2&x=1&signature=a%2fb+z",
		"http://cdn.invalid/movie%2B.mkv?z=0&sig=a%2fb+z&sig=next",
	}
	headers := http.Header{"User-Agent": {"Player/1.0 Full UA"}, "Range": {"bytes=0-1023"}}
	for _, name := range []string{"Authorization", "Cookie", "Authx", "X-Emby-Token", "X-Emby-Authorization", "X-Api-Key", "Referer"} {
		headers.Set(name, "media-server-secret")
	}
	var bodies []*externalTestBody
	transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
		hop := len(bodies)
		if hop >= len(urls) {
			return nil, errors.New("unexpected extra request")
		}
		if r.Method != http.MethodGet || r.URL.String() != urls[hop] || r.UserAgent() != headers.Get("User-Agent") || r.Header.Get("Range") != "bytes=0-1023" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("hop %d changed URL or required headers: %s %s %v", hop, r.Method, r.URL, r.Header)
		}
		for name := range r.Header {
			if name != "User-Agent" && name != "Range" && name != "Accept-Encoding" {
				t.Errorf("hop %d forwarded media-server header %q", hop, name)
			}
		}
		if hop > 0 && bodies[hop-1].closes != 1 {
			t.Errorf("hop %d started before previous response was closed", hop)
		}
		body := &externalTestBody{}
		bodies = append(bodies, body)
		response := &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), Body: body, ContentLength: 1 << 40}
		switch hop {
		case 0:
			response.StatusCode = http.StatusSeeOther
			response.Header.Set("Location", "../video%2Ftwo?x=2&x=1&signature=a%2fb+z")
		case 1:
			response.StatusCode = http.StatusTemporaryRedirect
			response.Header.Set("Location", "//cdn.invalid/movie%2B.mkv?z=0&sig=a%2fb+z&sig=next")
		}
		return response, nil
	})
	link, err := ResolveExternalLink(t.Context(), urls[0], headers, transport)
	if err != nil || link.URL != urls[2] || link.Status != http.StatusPartialContent || len(bodies) != len(urls) {
		t.Fatalf("resolution = %+v, err = %v, requests = %d", link, err, len(bodies))
	}
	for hop, body := range bodies {
		if body.reads != 0 || body.closes != 1 {
			t.Fatalf("hop %d body: reads=%d closes=%d", hop, body.reads, body.closes)
		}
	}
	if headers.Get("Authorization") != "media-server-secret" || headers.Get("Range") != "bytes=0-1023" {
		t.Fatal("resolver mutated incoming headers")
	}
}

func TestResolveExternalLinkResponseValidationAndCache(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, location string
		status                      int
		want                        bool
	}{
		{name: "200_ignores_range_without_type", status: 200, want: true},
		{name: "206_octet_stream", status: 206, contentType: "application/octet-stream", want: true},
		{name: "200_video", status: 200, contentType: "video/mp4", want: true},
		{name: "html", status: 200, contentType: "Text/HTML; charset=utf-8"},
		{name: "xhtml", status: 200, contentType: "application/xhtml+xml; charset=utf-8"},
		{name: "json", status: 200, contentType: "application/json"},
		{name: "json_suffix", status: 206, contentType: "application/problem+json"},
		{name: "forbidden", status: 403},
		{name: "limited", status: 429},
		{name: "unavailable", status: 503},
		{name: "missing_location", status: 303},
		{name: "non_http_location", status: 303, location: "file:///video"},
		{name: "userinfo_location", status: 303, location: "https://secret@cdn.invalid/video"},
		{name: "fragment_location", status: 303, location: "/video#part"},
		{name: "malformed_location", status: 303, location: "/%zz"},
		{name: "redirect_loop", status: 302, location: "/video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupExternalLinkTest(t)
			const raw, ua = "http://moviepilot/video", "Player/1.0"
			var bodies []*externalTestBody
			transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Range") != "bytes=0-0" {
					t.Error("missing bounded default range")
				}
				body := &externalTestBody{}
				bodies = append(bodies, body)
				header := http.Header{"Content-Type": {tc.contentType}, "Location": {tc.location}}
				return &http.Response{StatusCode: tc.status, Header: header, Body: body, ContentLength: 1 << 40}, nil
			})
			link, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
			if (err == nil) != tc.want {
				t.Fatalf("resolution = %+v, err = %v, want success=%t", link, err, tc.want)
			}
			if _, cached := CachedExternalLink(raw, ua); cached != tc.want {
				t.Fatalf("cached = %t, want %t", cached, tc.want)
			}
			wantCalls := 1
			if tc.name == "redirect_loop" {
				wantCalls = 11
			}
			if len(bodies) != wantCalls {
				t.Fatalf("requests = %d, want %d", len(bodies), wantCalls)
			}
			for _, body := range bodies {
				if body.reads != 0 || body.closes != 1 {
					t.Fatalf("response read or leaked: %+v", body)
				}
			}
		})
	}
}

func TestExternalLinkExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name, raw string
		want      time.Time
	}{
		{"generic_without_signature", "https://cdn.invalid/video", now.Add(10 * time.Minute)},
		{"generic_ignores_t", "https://cdn.invalid/video?t=invalid&t=0", now.Add(10 * time.Minute)},
		{"lookalike_host", "https://115cdn.net.invalid/video", now.Add(10 * time.Minute)},
		{"115_six_minutes", "https://a.115cdn.net/video?t=" + strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10), now.Add(time.Minute)},
		{"115_ten_minute_cap", "https://115cdn.net/video?t=" + strconv.FormatInt(now.Add(time.Hour).Unix(), 10), now.Add(10 * time.Minute)},
		{"115_missing_t", "https://a.115cdn.net/video", time.Time{}},
		{"115_duplicate_t", "https://a.115cdn.net/video?t=1800001000&t=1800002000", time.Time{}},
		{"115_invalid_t", "https://a.115cdn.net/video?t=invalid", time.Time{}},
		{"115_safety_boundary", "https://a.115cdn.net/video?t=" + strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10), time.Time{}},
		{"115_expired", "https://a.115cdn.net/video?t=1", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := externalLinkExpiresAt(tc.raw, now); !got.Equal(tc.want) {
				t.Fatalf("expiry = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExternalLinkCacheDoesNotRenewExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		const ua = "Player/1.0"
		raw := "https://a.115cdn.net/video?t=" + strconv.FormatInt(time.Now().Add(6*time.Minute).Unix(), 10)
		calls := 0
		transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		first, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
		if err != nil || !first.ExpiresAt.Equal(time.Now().Add(time.Minute)) {
			t.Fatalf("initial link = %+v, err = %v", first, err)
		}
		time.Sleep(30 * time.Second)
		hit, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
		if err != nil || hit != first || calls != 1 {
			t.Fatalf("cache hit renewed expiry or fetched again: %+v %v calls=%d", hit, err, calls)
		}
		time.Sleep(30 * time.Second)
		if _, cached := CachedExternalLink(raw, ua); cached {
			t.Fatal("signature safety deadline was extended by a hit")
		}
		if _, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport); err != nil || calls != 2 {
			t.Fatalf("expired result did not resolve again: calls=%d err=%v", calls, err)
		}
		if _, cached := CachedExternalLink(raw, ua); cached {
			t.Fatal("115 link inside its safety window was cached")
		}
	})
}

func TestExternalPreloadPublishesSharedCacheByFullUA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		DefaultPreloader = p
		const raw, ua = "http://moviepilot:3000/plugin/video?x=2&x=1", "Player/1.0 Full UA"
		calls := 0
		transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.UserAgent() != ua && r.UserAgent() != ua+" other" || r.Header.Get("Range") != "bytes=0-0" {
				t.Errorf("unexpected probe headers: %v", r.Header)
			}
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		candidate := externalTestCandidate(t, raw, ua, transport)
		if candidate.Account.AccountID != 0 || !p.Submit(candidate) {
			t.Fatal("third-party source required a local account")
		}
		synctest.Wait()
		cached, ok := CachedExternalLink(raw, ua)
		if !ok || len(db.Cache.Get(candidate.Source)) == 0 || calls != 1 {
			t.Fatalf("background did not publish shared db.Cache entry: %+v %t calls=%d", cached, ok, calls)
		}
		link, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
		if err != nil || link != cached || calls != 1 {
			t.Fatalf("ordinary playback fetched instead of sharing: %+v %v calls=%d", link, err, calls)
		}
		link, err = externalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport, func(ExternalLink) { t.Error("cache hit republished") })
		if err != nil || link != cached || calls != 1 {
			t.Fatalf("flight leader did not recheck cache: %+v %v calls=%d", link, err, calls)
		}
		for _, identity := range [][2]string{{raw, ua + " other"}, {"http://moviepilot:3000/plugin/video?x=1&x=2", ua}} {
			if _, ok := CachedExternalLink(identity[0], identity[1]); ok {
				t.Fatalf("cache crossed full-UA or raw-query identity: %q", identity)
			}
		}
		if _, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua + " other"}}, transport); err != nil || calls != 2 {
			t.Fatalf("different UA did not resolve independently: calls=%d err=%v", calls, err)
		}
	})
}

func TestResolveExternalLinkSharedCancellation(t *testing.T) {
	for _, mode := range []string{"leader", "waiter", "all"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				const raw, finalURL, ua = "http://moviepilot:3000/redirect", "https://cdn.invalid/video.mkv", "Player/1.0"
				release := make(chan struct{})
				calls := 0
				transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.String() == finalURL {
						return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
					}
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-release:
						return &http.Response{StatusCode: 302, Header: http.Header{"Location": {finalURL}}, Body: http.NoBody}, nil
					}
				})
				firstCtx, cancelFirst := context.WithCancel(t.Context())
				defer cancelFirst()
				secondCtx, cancelSecond := context.WithCancel(t.Context())
				defer cancelSecond()
				type resolved struct {
					link ExternalLink
					err  error
				}
				first, second := make(chan resolved, 1), make(chan resolved, 1)
				resolve := func(ctx context.Context, result chan<- resolved) {
					link, err := ResolveExternalLink(ctx, raw, http.Header{"User-Agent": {ua}}, transport)
					result <- resolved{link, err}
				}
				go resolve(firstCtx, first)
				synctest.Wait()
				go resolve(secondCtx, second)
				synctest.Wait()
				if calls != 1 || len(first) != 0 || len(second) != 0 {
					t.Fatalf("requests did not share the initial resolution: calls=%d first=%d second=%d", calls, len(first), len(second))
				}
				if mode == "leader" || mode == "all" {
					cancelFirst()
				}
				if mode == "waiter" || mode == "all" {
					cancelSecond()
				}
				synctest.Wait()
				close(release)
				synctest.Wait()
				if len(first) != 1 || len(second) != 1 {
					t.Fatalf("resolution did not finish: first=%d second=%d", len(first), len(second))
				}
				one, two := <-first, <-second
				if mode == "all" {
					if !errors.Is(one.err, context.Canceled) || !errors.Is(two.err, context.Canceled) || calls != 1 {
						t.Fatalf("all canceled results: first=%v second=%v calls=%d", one.err, two.err, calls)
					}
					if _, cached := CachedExternalLink(raw, ua); cached {
						t.Fatal("all canceled requests published a result")
					}
					return
				}
				live, canceled, wantCalls := one, two, 2
				if mode == "leader" {
					live, canceled, wantCalls = two, one, 3
				}
				if !errors.Is(canceled.err, context.Canceled) || live.err != nil || live.link.URL != finalURL || calls != wantCalls {
					t.Fatalf("one cancellation affected the live request: live=%+v canceled=%v calls=%d", live, canceled.err, calls)
				}
				if cached, ok := CachedExternalLink(raw, ua); !ok || cached != live.link {
					t.Fatalf("live request did not publish the shared result: %+v %t", cached, ok)
				}
			})
		})
	}
}

func TestResolveExternalLinkRepeatedLeaderCancellationKeepsBudget(t *testing.T) {
	for _, mode := range []string{"completes", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				const raw, ua = "http://moviepilot:3000/video", "Player/1.0"
				type requestID struct{}
				release := make(chan struct{})
				started := make(chan int, 3)
				deadline := time.Now().Add(15 * time.Second)
				transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
					if got, ok := r.Context().Deadline(); !ok || !got.Equal(deadline) {
						t.Errorf("resolution reset its deadline: %v, present=%t, want %v", got, ok, deadline)
					}
					started <- r.Context().Value(requestID{}).(int)
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-release:
						return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
					}
				})
				var cancel [3]context.CancelFunc
				var results [3]chan error
				for id := range results {
					ctx, stop := context.WithCancel(context.WithValue(t.Context(), requestID{}, id))
					cancel[id], results[id] = stop, make(chan error, 1)
					t.Cleanup(stop)
					go func() {
						_, err := ResolveExternalLink(ctx, raw, http.Header{"User-Agent": {ua}}, transport)
						results[id] <- err
					}()
					synctest.Wait()
				}
				if len(started) != 1 || <-started != 0 {
					t.Fatal("initial requests did not share the first leader")
				}
				time.Sleep(5 * time.Second)
				cancel[0]()
				synctest.Wait()
				if len(started) != 1 {
					t.Fatal("live waiters did not share a replacement leader")
				}
				secondLeader := <-started
				time.Sleep(5 * time.Second)
				cancel[secondLeader]()
				synctest.Wait()
				if len(started) != 1 {
					t.Fatal("remaining waiter stopped after the second canceled leader")
				}
				live := <-started
				if mode == "completes" {
					close(release)
				} else {
					time.Sleep(5 * time.Second)
				}
				synctest.Wait()
				for id, result := range results {
					if len(result) != 1 {
						t.Fatalf("request %d did not finish in its original budget", id)
					}
					err, want := <-result, error(context.Canceled)
					if id == live {
						want = nil
						if mode == "deadline" {
							want = context.DeadlineExceeded
						}
					}
					if !errors.Is(err, want) {
						t.Fatalf("request %d result = %v, want %v", id, err, want)
					}
				}
				if len(started) != 0 {
					t.Fatal("deadline or completed requests started extra work")
				}
				if _, cached := CachedExternalLink(raw, ua); cached != (mode == "completes") {
					t.Fatalf("cached=%t, mode=%s", cached, mode)
				}
			})
		})
	}
}

func TestResolveExternalLinkSharedFailureDoesNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
	}{
		{name: "forbidden", status: 403},
		{name: "unavailable", status: 503},
		{name: "connection_error", err: errors.New("connection failed")},
		{name: "transport_canceled_with_live_context", err: context.Canceled},
		{name: "transport_deadline_with_live_context", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				const raw, ua = "http://moviepilot:3000/video", "Player/1.0"
				release := make(chan struct{})
				calls := 0
				transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
					calls++
					<-release
					if tc.err != nil {
						return nil, tc.err
					}
					return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: http.NoBody}, nil
				})
				results := make(chan error, 2)
				for range 2 {
					go func() {
						_, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
						results <- err
					}()
					synctest.Wait()
				}
				close(release)
				synctest.Wait()
				if calls != 1 || len(results) != 2 || <-results == nil || <-results == nil {
					t.Fatalf("shared failure retried or did not finish: calls=%d", calls)
				}
				if _, cached := CachedExternalLink(raw, ua); cached {
					t.Fatal("failed resolution published a result")
				}
			})
		})
	}
}

func TestExternalPreloadTakeoverSharesInflightRemainingBudget(t *testing.T) {
	for _, mode := range []string{"completes", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				p := newTestPreloader(t)
				DefaultPreloader = p
				const raw, ua = "http://moviepilot/video", "Player/1.0"
				release := make(chan struct{})
				calls := 0
				transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) != 3*time.Second {
						t.Errorf("background request deadline = %v, present=%t", deadline, ok)
					}
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-release:
						return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
					}
				})
				if !p.Submit(externalTestCandidate(t, raw, ua, transport)) {
					t.Fatal("preload rejected")
				}
				synctest.Wait()
				time.Sleep(2 * time.Second)
				result := make(chan error, 1)
				go func() {
					_, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
					result <- err
				}()
				synctest.Wait()
				if calls != 1 || len(result) != 0 {
					t.Fatalf("takeover did not join in-flight request: calls=%d completed=%d", calls, len(result))
				}
				if mode == "completes" {
					time.Sleep(500 * time.Millisecond)
					close(release)
				} else {
					time.Sleep(time.Second)
				}
				synctest.Wait()
				if len(result) != 1 {
					t.Fatalf("takeover exceeded the remaining background budget: mode=%s", mode)
				}
				err := <-result
				if mode == "completes" && err != nil || mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
					t.Fatalf("takeover result = %v calls=%d", err, calls)
				}
				if _, cached := CachedExternalLink(raw, ua); cached != (mode == "completes") {
					t.Fatalf("takeover cached=%t, mode=%s", cached, mode)
				}
			})
		})
	}
}

func TestExternalPreloadRevocationPreventsLatePublish(t *testing.T) {
	for _, mode := range []string{"cancel", "generation"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				p := newTestPreloader(t)
				const raw, ua = "http://moviepilot/video", "Player/1.0"
				release := make(chan struct{})
				calls := 0
				body := &externalTestBody{}
				transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
					calls++
					<-release // 故意模拟不及时响应取消的上游。
					return &http.Response{StatusCode: 206, Header: make(http.Header), Body: body}, nil
				})
				candidate := externalTestCandidate(t, raw, ua, transport)
				if !p.Submit(candidate) {
					t.Fatal("preload rejected")
				}
				synctest.Wait()
				if calls != 1 {
					t.Fatal("request never started")
				}
				if mode == "cancel" {
					p.CancelScope(candidate.Scope)
				} else {
					p.Apply(true, 2)
				}
				synctest.Wait()
				close(release)
				synctest.Wait()
				if _, cached := CachedExternalLink(raw, ua); cached || len(db.Cache.Get(candidate.Source)) != 0 || body.closes != 1 {
					t.Fatalf("late response published or leaked after %s: cached=%t body=%+v", mode, cached, body)
				}
			})
		})
	}
}

func TestWaitExternalLinkOnlyWaitsForExistingWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		const raw, ua = "http://moviepilot/video", "Player/1.0"
		before := time.Now()
		if _, ok := WaitExternalLink(t.Context(), raw, ua); ok || !time.Now().Equal(before) {
			t.Fatal("cache miss started work or waited without an active request")
		}
		release := make(chan struct{})
		calls := 0
		transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			<-release
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		if !p.Submit(externalTestCandidate(t, raw, ua, transport)) {
			t.Fatal("preload rejected")
		}
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("background calls = %d, want 1", calls)
		}
		if _, ok := WaitExternalLink(t.Context(), raw, ua+" other"); ok || !time.Now().Equal(before) {
			t.Fatal("passive wait crossed UA identity or waited for an unrelated request")
		}
		result, canceled := make(chan bool, 1), make(chan bool, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, ok := WaitExternalLink(t.Context(), raw, ua)
			result <- ok
		}()
		go func() {
			_, ok := WaitExternalLink(ctx, raw, ua)
			canceled <- ok
		}()
		synctest.Wait()
		if len(result) != 0 || len(canceled) != 0 {
			t.Fatal("passive wait returned before the active request completed")
		}
		cancel()
		synctest.Wait()
		if len(canceled) != 1 || <-canceled {
			t.Fatal("canceled passive wait did not fall back")
		}
		if calls != 1 || len(result) != 0 {
			t.Fatal("canceling one waiter affected the leader or another waiter")
		}
		close(release)
		synctest.Wait()
		if len(result) != 1 || !<-result || calls != 1 {
			t.Fatalf("passive waiter did not receive the shared result: calls=%d", calls)
		}
		link, ok := WaitExternalLink(t.Context(), raw, ua)
		if !ok || link.URL != raw || calls != 1 {
			t.Fatalf("passive cache hit = %+v %t, calls=%d", link, ok, calls)
		}
	})
}

func TestWaitExternalLinkHasThreeSecondLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		const raw, ua = "http://moviepilot/video", "Player/1.0"
		release := make(chan struct{})
		calls := 0
		transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			<-release
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		resolved := make(chan error, 1)
		go func() {
			_, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {ua}}, transport)
			resolved <- err
		}()
		synctest.Wait()
		waited := make(chan bool, 1)
		go func() {
			_, ok := WaitExternalLink(t.Context(), raw, ua)
			waited <- ok
		}()
		synctest.Wait()
		time.Sleep(3*time.Second - time.Millisecond)
		synctest.Wait()
		if len(waited) != 0 {
			t.Fatal("passive waiter returned before its budget expired")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(waited) != 1 || <-waited || calls != 1 {
			t.Fatalf("passive wait exceeded three seconds or started another request: calls=%d", calls)
		}
		close(release)
		synctest.Wait()
		if len(resolved) != 1 || <-resolved != nil {
			t.Fatal("passive timeout canceled the ordinary leader")
		}
	})
}

func TestExternalPreloadOriginSpacingAndSignatureGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		type start struct {
			raw string
			at  time.Time
		}
		started := make(chan start, 3)
		release := make(chan struct{})
		transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
			started <- start{r.URL.String(), time.Now()}
			if r.URL.Path == "/first" {
				<-release
			}
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		const first, second, other = "http://moviepilot/first", "http://moviepilot/second", "http://other/video"
		for _, raw := range []string{first, second, other} {
			if !p.Submit(externalTestCandidate(t, raw, "Player/1.0", transport)) {
				t.Fatalf("candidate rejected: %s", raw)
			}
			if raw == first {
				synctest.Wait()
			}
		}
		synctest.Wait()
		starts := make(map[string]time.Time)
		for len(started) > 0 {
			event := <-started
			starts[event.raw] = event.at
		}
		if len(starts) != 2 || starts[first].IsZero() || starts[other].IsZero() || !starts[second].IsZero() {
			t.Fatalf("same origin overlapped or blocked a distinct origin: %v", starts)
		}
		close(release)
		synctest.Wait()
		time.Sleep(10*time.Second - time.Millisecond)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("same origin bypassed ten-second spacing")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(started) != 1 {
			t.Fatalf("same-origin request did not start after spacing: starts=%d", len(started))
		}
		next := <-started
		if next.raw != second || next.at.Sub(starts[first]) != 10*time.Second {
			t.Fatalf("same-origin request = %+v, first=%v", next, starts[first])
		}
		for _, account := range []PreloadAccount{{Provider: "115", UserID: "real-user"}, ExternalPreloadAccount(first)} {
			key := "signature-guard-" + account.Provider
			candidate := preloadCandidate(key, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				if task.BeginSignature(account, key) {
					t.Error("BeginSignature accepted an account without AccountID")
				}
				return PreloadResult{Outcome: PreloadSkipped}
			})
			candidate.Scope = key
			if !p.Submit(candidate) {
				t.Fatal("signature guard candidate rejected")
			}
			synctest.Wait()
		}
	})
}

func TestExternalPreload429CoolsOnlyItsOrigin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		calls := 0
		transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path == "/limited" {
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"1200"}}, Body: http.NoBody}, nil
			}
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		if !p.Submit(externalTestCandidate(t, "http://moviepilot/limited", "Player/1.0", transport)) {
			t.Fatal("initial candidate rejected")
		}
		synctest.Wait()
		blocked := externalTestCandidate(t, "http://moviepilot/other", "Player/1.0", transport)
		if p.Submit(blocked) || !p.Submit(externalTestCandidate(t, "http://other/video", "Player/1.0", transport)) {
			t.Fatal("429 cooldown did not isolate its origin")
		}
		synctest.Wait()
		time.Sleep(10 * time.Minute)
		if p.Submit(blocked) || calls != 2 {
			t.Fatalf("Retry-After was shortened or request retried without a signal: calls=%d", calls)
		}
		time.Sleep(10 * time.Minute)
		if !p.Submit(blocked) {
			t.Fatal("origin stayed blocked after Retry-After")
		}
		synctest.Wait()
		if calls != 3 {
			t.Fatalf("released origin calls=%d, want 3", calls)
		}
	})
}

func TestExternalPreloadTransientRetryNeedsNewSignalAndKeepsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		calls := 0
		transport := externalTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset")
		})
		candidate := externalTestCandidate(t, "http://moviepilot/video", "Player/1.0", transport)
		if !p.Submit(candidate) {
			t.Fatal("initial candidate rejected")
		}
		synctest.Wait()
		if calls != 1 || p.Submit(candidate) {
			t.Fatalf("network failure retried before cooldown: calls=%d", calls)
		}
		time.Sleep(30*time.Second - time.Millisecond)
		synctest.Wait()
		if calls != 1 || p.Submit(candidate) {
			t.Fatal("network cooldown was shorter than thirty seconds")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("network failure retried without a new signal")
		}
		if !p.Submit(candidate) {
			t.Fatal("new signal after cooldown did not permit the second attempt")
		}
		synctest.Wait()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls != 2 || p.Submit(candidate) {
			t.Fatalf("network failure exceeded two attempts: calls=%d", calls)
		}
	})
}

func TestExternalPreloadFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want PreloadOutcome
	}{
		{"unauthorized", externalStatusError{status: 401}, PreloadRisk},
		{"forbidden", externalStatusError{status: 403}, PreloadRisk},
		{"rate_limit", externalStatusError{status: 429, retryAfter: time.Hour}, PreloadRisk},
		{"bad_gateway", externalStatusError{status: 502}, PreloadTransient},
		{"unavailable", externalStatusError{status: 503}, PreloadTransient},
		{"gateway_timeout", externalStatusError{status: 504}, PreloadTransient},
		{"bad_request", externalStatusError{status: 400}, PreloadFailed},
		{"server_error", externalStatusError{status: 500}, PreloadFailed},
		{"network", fmt.Errorf("resolve: %w", &url.Error{Op: "Get", URL: "https://secret.invalid?token=secret", Err: errors.New("connection reset")}), PreloadTransient},
		{"canceled", &url.Error{Err: context.Canceled}, PreloadFailed},
		{"deadline", &url.Error{Err: context.DeadlineExceeded}, PreloadFailed},
		{"invalid_link", errors.New("invalid external location"), PreloadFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := externalPreloadFailure(tc.err)
			if got.Outcome != tc.want || got.StatusSource != "第三方" || strings.Contains(got.Reason, "secret") {
				t.Fatalf("failure classification = %+v, want outcome=%v", got, tc.want)
			}
			if status, ok := errors.AsType[externalStatusError](tc.err); ok && (got.HTTPStatus != status.status || got.RetryAfter != status.retryAfter) {
				t.Fatalf("lost HTTP diagnostics: %+v", got)
			}
		})
	}
}
