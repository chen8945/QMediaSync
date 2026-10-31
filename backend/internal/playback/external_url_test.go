package playback

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestNormalizeExternalURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"Docker查询", "http://moviepilot:3000/redirect?name=电影 1.mkv&sign=a%2fb+z&x=2&x=1", "http://moviepilot:3000/redirect?name=%E7%94%B5%E5%BD%B1%201.mkv&sign=a%2fb+z&x=2&x=1"},
		{"混合路径编码", "http://openlist:5244/d/电影%2f1.mkv?x=&x=1&&", "http://openlist:5244/d/%E7%94%B5%E5%BD%B1%2f1.mkv?x=&x=1&&"},
		{"路径竖线", "http://source/d/电影%2f1|2.mkv?sign=keep%2fb", "http://source/d/%E7%94%B5%E5%BD%B1%2f1%7C2.mkv?sign=keep%2fb"},
		{"路径花括号", "http://source/d/电影%2f{1}.mkv?sign=keep%2fb", "http://source/d/%E7%94%B5%E5%BD%B1%2f%7B1%7D.mkv?sign=keep%2fb"},
		{"路径反斜杠", `http://source/d/电影%2f1\2.mkv?sign=keep%2fb`, "http://source/d/%E7%94%B5%E5%BD%B1%2f1%5C2.mkv?sign=keep%2fb"},
		{"路径双重百分号", "http://source/d/电影%252f1|2.mkv?sign=keep%2fb", "http://source/d/%E7%94%B5%E5%BD%B1%252f1%7C2.mkv?sign=keep%2fb"},
		{"签名路径", "http://litepan/api/strm/play/1/file/t/token/n/%E7%94%B5%E5%BD%B1%3B01.mkv/s/sig", "http://litepan/api/strm/play/1/file/t/token/n/%E7%94%B5%E5%BD%B1%3B01.mkv/s/sig"},
		{"空查询串", "http://source/video?", "http://source/video?"},
		{"保留查询编码", "http://source/%41?name=a%2520b%26c%2Bd&literal=%", "http://source/%41?name=a%2520b%26c%2Bd&literal=%"},
		{"相对路径", "/video", ""},
		{"非HTTP", "file:///video", ""},
		{"用户凭据", "http://user:password@source/video", ""},
		{"片段", "http://source/video#fragment", ""},
		{"查询控制字符", "http://source/video?name=a\nb", ""},
		{"删除控制字符", "http://source/video?name=a\x7fb", ""},
		{"无效路径编码", "http://source/video%zz", ""},
		{"超限原始地址", "http://source/" + strings.Repeat("x", 16<<10), ""},
		{"编码后超限地址", "http://source/?name=" + strings.Repeat("中", 3000), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeExternalURL(tc.raw)
			if ok != (tc.want != "") || ok && got != tc.want {
				t.Fatalf("normalized=%q valid=%t, want %q", got, ok, tc.want)
			}
			if ok {
				if again, valid := NormalizeExternalURL(got); !valid || again != got {
					t.Fatalf("normalization is not idempotent: %q valid=%t", again, valid)
				}
			}
		})
	}
}

func TestExternalLinkIdentityPreservesURLMeaning(t *testing.T) {
	const ua = "Player/1.0 Full UA"
	for _, tc := range []struct {
		name, first, second string
		shared              bool
	}{
		{"MoviePilot查询中文空格", "http://moviepilot:3000/redirect?pickcode=opaque&file_name=电影 1.mkv&x=2&x=1", "http://moviepilot:3000/redirect?pickcode=opaque&file_name=%E7%94%B5%E5%BD%B1%201.mkv&x=2&x=1", true},
		{"OpenList路径中文空格", "http://openlist:5244/d/电影 1.mkv?sign=a%2fb+z", "http://openlist:5244/d/%E7%94%B5%E5%BD%B1%201.mkv?sign=a%2fb+z", true},
		{"编码斜杠", "http://source/d/a%2Fb", "http://source/d/a/b", false},
		{"竖线路径不能消除编码斜杠", "http://source/d/电影%2f1|2.mkv", "http://source/d/电影/1%7C2.mkv", false},
		{"花括号路径不能消除编码斜杠", "http://source/d/电影%2f{1}.mkv", "http://source/d/电影/%7B1%7D.mkv", false},
		{"反斜杠路径不能消除编码斜杠", `http://source/d/电影%2f1\2.mkv`, "http://source/d/电影/1%5C2.mkv", false},
		{"编码分号", "http://source/d/a%3Bb", "http://source/d/a;b", false},
		{"编码查询分隔符", "http://source/video?key=a%26b", "http://source/video?key=a&b", false},
		{"加号与空格", "http://source/video?name=a+b", "http://source/video?name=a%20b", false},
		{"已有编码大小写", "http://source/video?key=%2f", "http://source/video?key=%2F", false},
		{"未保留字符的已有编码", "http://source/%41", "http://source/A", false},
		{"重复查询参数", "http://source/video?x=1&x=2", "http://source/video?x=1", false},
		{"查询顺序", "http://source/video?x=1&x=2", "http://source/video?x=2&x=1", false},
		{"空查询参数", "http://source/video?x=&flag&&x=2&", "http://source/video?x=&flag&x=2", false},
		{"空查询串", "http://source/video?", "http://source/video", false},
		{"双重编码", "http://source/video?name=a%2520b", "http://source/video?name=a%20b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared := ExternalLinkIdentity(tc.first, ua) == ExternalLinkIdentity(tc.second, ua)
			if shared != tc.shared {
				t.Fatalf("identity shared=%t, want %t", shared, tc.shared)
			}
			if ExternalLinkIdentity(tc.first, ua) == ExternalLinkIdentity(tc.first, ua+" Other") {
				t.Fatal("different full UAs shared an identity")
			}
		})
	}
}

func TestExternalURLPreservesAllValidPathASCII(t *testing.T) {
	// 与 net/url 的 validEncoded 路径集合对照，已有转义也必须保持独立身份。
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~!$&'()*+,;=:@/[]"
	for _, c := range []byte(allowed) {
		raw := "http://source/d/a" + string(c) + "b%2fc.mkv?sign=keep%2fb"
		if got, ok := NormalizeExternalURL(raw); !ok || got != raw {
			t.Errorf("valid path character %q changed: %q valid=%t", c, got, ok)
		}
		encoded := fmt.Sprintf("http://source/d/a%%%02Xb%%2fc.mkv?sign=keep%%2fb", c)
		if ExternalLinkIdentity(raw, "Player/1.0") == ExternalLinkIdentity(encoded, "Player/1.0") {
			t.Errorf("literal %q and existing percent encoding share an identity", c)
		}
	}
	const raw = "http://source/d/电影%2f" + allowed + " \"<>\\^`{|}%21%27%28%29%2A%5B%5D%25%2525.mkv?sign=keep%2fb"
	const want = "http://source/d/%E7%94%B5%E5%BD%B1%2f" + allowed + "%20%22%3C%3E%5C%5E%60%7B%7C%7D%21%27%28%29%2A%5B%5D%25%2525.mkv?sign=keep%2fb"
	if got, ok := NormalizeExternalURL(raw); !ok || got != want {
		t.Fatalf("mixed valid and invalid path bytes changed: %q valid=%t", got, ok)
	}
	if got, ok := NormalizeExternalURL(want); !ok || got != want {
		t.Fatalf("mixed escaped path is not idempotent: %q valid=%t", got, ok)
	}
}

func TestResolveExternalLinkNormalizesEveryRequest(t *testing.T) {
	setupExternalLinkTest(t)
	const sourcePath = "/redirect?file_name=电影 1.mkv&sign=a%2fb+z&x=2&x=1"
	const sourceEncoded = "/redirect?file_name=%E7%94%B5%E5%BD%B1%201.mkv&sign=a%2fb+z&x=2&x=1"
	const targetPath = "/d/电影%2F1.mkv?file_name=电影 1.mkv&sign=a%2fb+z&x=2&x=1"
	const targetEncoded = "/d/%E7%94%B5%E5%BD%B1%2F1.mkv?file_name=%E7%94%B5%E5%BD%B1%201.mkv&sign=a%2fb+z&x=2&x=1"
	const ua = "Player/1.0"
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.RequestURI
		if r.UserAgent() != ua || r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("unexpected request headers: %v", r.Header)
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", targetPath)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer server.Close()
	link, err := ResolveExternalLink(t.Context(), server.URL+sourcePath, http.Header{"User-Agent": {ua}}, server.Client().Transport)
	if err != nil || link.URL != server.URL+targetEncoded {
		t.Fatalf("resolved=%+v err=%v", link, err)
	}
	for _, want := range []string{sourceEncoded, targetEncoded} {
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("request URI=%q, want %q", got, want)
			}
		default:
			t.Fatal("missing request")
		}
	}
	for _, raw := range []string{server.URL + sourcePath, server.URL + sourceEncoded} {
		cached, ok := CachedExternalLink(raw, ua)
		if !ok || cached.URL != link.URL || cached.Status != link.Status || !cached.ExpiresAt.Equal(link.ExpiresAt) {
			t.Fatalf("equivalent source did not share result: %+v cached=%t", cached, ok)
		}
	}
}

func TestResolveExternalLinkPreservesMixedPathEscapes(t *testing.T) {
	for _, tc := range []struct {
		name, raw, encoded string
	}{
		{"竖线", "/d/电影%2f1|2.mkv?sign=keep%2fb", "/d/%E7%94%B5%E5%BD%B1%2f1%7C2.mkv?sign=keep%2fb"},
		{"花括号", "/d/电影%2f{1}.mkv?sign=keep%2fb", "/d/%E7%94%B5%E5%BD%B1%2f%7B1%7D.mkv?sign=keep%2fb"},
		{"反斜杠", `/d/电影%2f1\2.mkv?sign=keep%2fb`, "/d/%E7%94%B5%E5%BD%B1%2f1%5C2.mkv?sign=keep%2fb"},
		{"合法字面字符与已有编码", "/d/电影%2f[01]!()'*%21%27%28%29%2A%5B%5D.mkv?sign=keep%2fb", "/d/%E7%94%B5%E5%BD%B1%2f[01]!()'*%21%27%28%29%2A%5B%5D.mkv?sign=keep%2fb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupExternalLinkTest(t)
			requests := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.RequestURI
				if r.URL.Path == "/redirect" {
					w.Header().Set("Location", tc.raw)
					w.WriteHeader(http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "video/x-matroska")
				w.WriteHeader(http.StatusPartialContent)
			}))
			defer server.Close()
			for _, entry := range []string{tc.raw, "/redirect"} {
				link, err := ResolveExternalLink(t.Context(), server.URL+entry, http.Header{"User-Agent": {"Player/1.0"}}, server.Client().Transport)
				if err != nil || link.URL != server.URL+tc.encoded {
					t.Fatalf("entry=%q link=%+v err=%v", entry, link, err)
				}
			}
			for _, want := range []string{tc.encoded, "/redirect", tc.encoded} {
				select {
				case got := <-requests:
					if got != want {
						t.Fatalf("request URI=%q, want %q", got, want)
					}
				default:
					t.Fatal("missing request")
				}
			}
		})
	}
}

func TestExternalPreloadSharesEncodingEquivalentInflight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupExternalLinkTest(t)
		p := newTestPreloader(t)
		DefaultPreloader = p
		const raw = "http://moviepilot:3000/redirect?file_name=电影 1.mkv&sign=a%2fb+z&x=2&x=1"
		const encoded = "http://moviepilot:3000/redirect?file_name=%E7%94%B5%E5%BD%B1%201.mkv&sign=a%2fb+z&x=2&x=1"
		const ua = "Player/1.0 Full UA"
		release := make(chan struct{})
		calls := 0
		transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != encoded || ExternalLinkIdentity(r.URL.String(), r.UserAgent()) != ExternalLinkIdentity(raw, r.UserAgent()) {
				t.Errorf("request URL and identity differ: %s", r.URL)
			}
			if calls == 1 {
				<-release
			}
			return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody}, nil
		})
		candidate := externalTestCandidate(t, raw, ua, transport)
		other := externalTestCandidate(t, encoded, ua, transport)
		if candidate.Source != other.Source || !p.Submit(candidate) {
			t.Fatal("equivalent preload identity rejected")
		}
		synctest.Wait()
		resolved := make(chan error, 1)
		go func() {
			_, err := ResolveExternalLink(t.Context(), encoded, http.Header{"User-Agent": {ua}}, transport)
			resolved <- err
		}()
		waited := make(chan bool, 1)
		go func() {
			_, ok := WaitExternalLink(t.Context(), encoded, ua)
			waited <- ok
		}()
		synctest.Wait()
		if calls != 1 || len(resolved) != 0 || len(waited) != 0 {
			t.Fatalf("equivalent request missed in-flight resolution: calls=%d", calls)
		}
		close(release)
		synctest.Wait()
		if <-resolved != nil || !<-waited {
			t.Fatal("shared in-flight request failed")
		}
		for _, source := range []string{raw, encoded} {
			if link, err := ResolveExternalLink(t.Context(), source, http.Header{"User-Agent": {ua}}, transport); err != nil || link.URL != encoded || calls != 1 {
				t.Fatalf("equivalent source missed cache: %+v err=%v calls=%d", link, err, calls)
			}
		}
		if _, err := ResolveExternalLink(t.Context(), encoded, http.Header{"User-Agent": {ua + " Other"}}, transport); err != nil || calls != 2 {
			t.Fatalf("different UA did not resolve independently: %v calls=%d", err, calls)
		}
	})
}

func TestResolveExternalLinkPreservesLitePanPathSignature(t *testing.T) {
	setupExternalLinkTest(t)
	// LitePan 对包含文件名的原始转义路径验签，合法字面字符和已有编码均不能改变。
	const path = "/api/strm/play/1/ZmlsZQ/t/test-token/n/%E7%94%B5%E5%BD%B1%3B[01]!()'*%21%27%28%29%2A%5B%5D.mkv"
	mac := hmac.New(sha256.New, []byte("fixture-secret"))
	mac.Write([]byte(path))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != path+"/s/"+signature {
			t.Errorf("signed request URI changed: %s", r.RequestURI)
		}
		unsigned := strings.TrimSuffix(r.URL.EscapedPath(), "/s/"+signature)
		check := hmac.New(sha256.New, []byte("fixture-secret"))
		check.Write([]byte(unsigned))
		if base64.RawURLEncoding.EncodeToString(check.Sum(nil)) != signature {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer server.Close()
	raw := server.URL + path + "/s/" + signature
	link, err := ResolveExternalLink(t.Context(), raw, http.Header{"User-Agent": {"Player/1.0"}}, server.Client().Transport)
	if err != nil || link.URL != raw {
		t.Fatalf("signed path changed: %+v err=%v", link, err)
	}
}

func TestExternalPreloadTenSecondBudget(t *testing.T) {
	for _, mode := range []string{"completes", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupExternalLinkTest(t)
				p := newTestPreloader(t)
				DefaultPreloader = p
				const raw, ua = "http://source/video", "Player/1.0"
				release := make(chan struct{})
				finished := make(chan time.Duration, 1)
				calls := 0
				transport := externalTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					started := time.Now()
					select {
					case <-release:
					case <-r.Context().Done():
					}
					finished <- time.Since(started)
					return &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), Body: http.NoBody}, nil
				})
				if !p.Submit(externalTestCandidate(t, raw, ua, transport)) {
					t.Fatal("preload rejected")
				}
				synctest.Wait()
				time.Sleep(7 * time.Second)
				synctest.Wait()
				if len(finished) != 0 {
					t.Fatal("preload expired before its ten-second deadline")
				}
				wantElapsed := 7 * time.Second
				if mode == "completes" {
					close(release)
				} else {
					time.Sleep(3*time.Second - time.Millisecond)
					synctest.Wait()
					if len(finished) != 0 {
						t.Fatal("preload expired before ten seconds")
					}
					time.Sleep(time.Millisecond)
					wantElapsed = 10 * time.Second
				}
				synctest.Wait()
				if len(finished) != 1 || <-finished != wantElapsed || calls != 1 {
					t.Fatalf("preload did not keep its execution budget: mode=%s calls=%d", mode, calls)
				}
				if _, cached := CachedExternalLink(raw, ua); cached != (mode == "completes") {
					t.Fatalf("late successful response publication: cached=%t mode=%s", cached, mode)
				}
			})
		})
	}
}
