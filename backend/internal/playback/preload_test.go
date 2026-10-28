package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"
)

var preloadTestAccount = PreloadAccount{Provider: "115", AccountID: 1, UserID: "test-user"}

func newTestPreloader(t *testing.T) *Preloader {
	t.Helper()
	p := NewPreloader()
	p.Start(context.Background())
	p.Apply(true, 1)
	t.Cleanup(p.Stop)
	return p
}

func preloadCandidate(key string, kind PreloadKind, run func(context.Context, *PreloadTask) PreloadResult) PreloadCandidate {
	return PreloadCandidate{Key: key, Scope: "device", Instance: "instance", Source: key, Kind: kind, Run: run}
}

func preloadLogger(t *testing.T, writer io.Writer, level helpers.LogLevel) {
	t.Helper()
	old, oldLevel := helpers.AppLogger, helpers.ConfiguredLogLevel()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(writer, "", 0)}
	helpers.SetGlobalLogLevel(level)
	t.Cleanup(func() {
		helpers.AppLogger = old
		helpers.SetGlobalLogLevel(oldLevel)
	})
}

func preloadSize(p *Preloader) (states, queued int, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.states) + len(p.accounts) + len(p.takeovers), len(p.queue), len(p.active) > 0
}

func TestPreloadDynamicWindow(t *testing.T) {
	for _, test := range []struct{ minutes, seconds int }{
		{2, 60}, {5, 150}, {6, 180}, {10, 180}, {30, 180}, {45, 270}, {60, 360}, {90, 540}, {120, 600}, {180, 600}, {10000, 600},
	} {
		duration := time.Duration(test.minutes) * time.Minute
		window := time.Duration(test.seconds) * time.Second
		if actual := PreloadWindow(duration); actual != window {
			t.Fatalf("%d 分钟的窗口=%v，期望 %v", test.minutes, actual, window)
		}
		for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
			position := duration - window + delta
			if got := ShouldPreloadNext(duration, position); got != (delta >= 0) {
				t.Fatalf("%v，边界偏移 %v，触发=%v", duration, delta, got)
			}
		}
		if test.minutes >= 60 && ShouldPreloadNext(duration, duration/5*4) {
			t.Fatalf("长内容不应仅到 80%% 就触发：%v", duration)
		}
	}
	for _, input := range [][2]time.Duration{{0, 0}, {-1, 0}, {time.Minute, -1}, {time.Minute, time.Minute}, {time.Minute, 2 * time.Minute}} {
		if ShouldPreloadNext(input[0], input[1]) {
			t.Fatalf("非法时长或进度触发：%v", input)
		}
	}
}

func TestPreloadDisabledAndSingleLatestDetail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPreloader()
		p.Start(context.Background())
		defer p.Stop()
		ran := make(chan string, 10)
		candidate := func(key, value string) PreloadCandidate {
			return preloadCandidate(key, PreloadDetail, func(context.Context, *PreloadTask) PreloadResult {
				ran <- value
				return PreloadResult{Outcome: PreloadCached}
			})
		}
		states, _, _ := preloadSize(p)
		if p.Submit(candidate("A", "disabled")) || states != 0 {
			t.Fatal("关闭时不能登记候选或调用回调")
		}
		p.Apply(true, 1)
		p.Submit(candidate("A", "A"))
		time.Sleep(time.Second)
		p.Submit(candidate("B", "B"))
		time.Sleep(time.Second)
		p.Submit(candidate("C", "old-C"))
		time.Sleep(1500 * time.Millisecond)
		p.Submit(candidate("C", "enriched-C"))
		time.Sleep(499 * time.Millisecond)
		synctest.Wait()
		if len(ran) != 0 {
			t.Fatalf("2 秒前执行了 %v", ran)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(ran) != 1 {
			t.Fatalf("只执行最后详情且补字段不延迟：执行次数=%d", len(ran))
		}
		if value := <-ran; value != "enriched-C" {
			t.Fatalf("未使用补齐后的回调：%s", value)
		}
	})
}

func TestPreloadConcurrentDuplicatesAndQueueBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		var calls atomic.Int32
		candidate := preloadCandidate("same", PreloadDetail, func(_ context.Context, task *PreloadTask) PreloadResult {
			if !task.BeginSignature(preloadTestAccount, "same") || !task.Publish(func() { calls.Add(1) }) {
				t.Error("唯一签名未获准")
			}
			return PreloadResult{Outcome: PreloadPublished}
		})
		var wait sync.WaitGroup
		for range 100 {
			wait.Go(func() { p.Submit(candidate) })
		}
		wait.Wait()
		_, queued, _ := preloadSize(p)
		if queued != 1 {
			t.Fatalf("重复候选占用队列=%d", queued)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 || p.Submit(candidate) {
			t.Fatal("成功身份不能被重复签名")
		}
		accepted := 0
		for i := range 100 {
			c := preloadCandidate(fmt.Sprint(i), PreloadDetail, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
			c.Scope = c.Key
			if p.Submit(c) {
				accepted++
			}
		}
		_, queued, _ = preloadSize(p)
		if accepted != preloadQueueLimit || queued != preloadQueueLimit {
			t.Fatalf("队列上限失效：accepted=%d queue=%d", accepted, queued)
		}
	})
}

func TestPreloadRapidBrowsingDoesNotFillHistoryWithUnusedCandidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		for i := range 1000 {
			candidate := preloadCandidate(fmt.Sprint(i), PreloadDetail, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
			if !p.Submit(candidate) {
				t.Fatalf("未执行的旧详情不应耗尽状态：%d", i)
			}
		}
		states, queued, _ := preloadSize(p)
		if states != 1 || queued != 1 {
			t.Fatalf("只保留最后候选：states=%d queued=%d", states, queued)
		}
	})
}

func TestPreloadAccountWaitRetainsPreparationAndDoesNotBlockOtherAccounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		var starts []time.Time
		prepared, runs, other := 0, 0, 0
		makeCandidate := func(key string) PreloadCandidate {
			ready := false
			return preloadCandidate(key, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				runs++
				if !ready {
					prepared++
					ready = true
				}
				if !task.BeginSignature(preloadTestAccount, key) {
					return PreloadResult{Outcome: PreloadSkipped}
				}
				starts = append(starts, time.Now())
				task.Publish(func() {})
				return PreloadResult{Outcome: PreloadPublished}
			})
		}
		p.Submit(makeCandidate("first"))
		synctest.Wait()
		p.Submit(makeCandidate("second"))
		synctest.Wait()
		_, queued, _ := preloadSize(p)
		if len(starts) != 1 || queued != 1 || prepared != 2 {
			t.Fatalf("账号间隔应退回队列：starts=%v prepared=%d queue=%d", starts, prepared, queued)
		}
		c := preloadCandidate("other", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			account := preloadTestAccount
			account.AccountID++
			account.UserID = "other-user"
			if task.BeginSignature(account, "other") {
				other++
				task.Publish(func() {})
			}
			return PreloadResult{Outcome: PreloadPublished}
		})
		c.Scope = "other-device"
		p.Submit(c)
		synctest.Wait()
		if other != 1 {
			t.Fatal("账号等待不应占用 worker")
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if len(starts) != 2 || starts[1].Sub(starts[0]) != preloadAccountGap || prepared != 2 || runs != 3 {
			t.Fatalf("间隔或准备复用错误：starts=%v prepared=%d runs=%d", starts, prepared, runs)
		}
	})
}

func TestPreloadSignatureRetryNeedsNewSignalAndRetainsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		var logs bytes.Buffer
		preloadLogger(t, &logs, helpers.LogLevelDebug)
		metadata, signatures, replaced := 0, 0, 0
		prepared := false
		candidate := preloadCandidate("retry", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			if !prepared {
				for range 2 {
					if err := task.Metadata(ctx, true, func(context.Context) (bool, error) { metadata++; return false, nil }); err != nil {
						t.Error(err)
					}
				}
				prepared = true
			} else if err := task.Metadata(ctx, true, func(context.Context) (bool, error) { metadata++; return false, nil }); !errors.Is(err, ErrPreloadMetadataBudget) {
				t.Errorf("第二次机会不能重置元数据：%v", err)
			}
			if task.BeginSignature(preloadTestAccount, "retry") {
				signatures++
			}
			return PreloadResult{Outcome: PreloadTransient, HTTPStatus: 503}
		})
		p.Submit(candidate)
		synctest.Wait()
		if strings.Contains(logs.String(), "[WARN]") {
			t.Fatal("首次瞬时失败不应 Warn")
		}
		time.Sleep(29 * time.Second)
		if p.Submit(candidate) {
			t.Fatal("冷却前不应接受重入")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if signatures != 1 {
			t.Fatal("没有新信号时不应自行重试")
		}
		candidate.Run = func(context.Context, *PreloadTask) PreloadResult { replaced++; return PreloadResult{} }
		if !p.Submit(candidate) {
			t.Fatal("冷却后新信号应允许唯一第二次机会")
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		if p.Submit(candidate) || signatures != 2 || metadata != 2 || replaced != 0 || strings.Count(logs.String(), "[WARN]") != 1 {
			t.Fatalf("重试上限、额度、闭包或日志错误：sign=%d meta=%d replaced=%d logs=%s", signatures, metadata, replaced, logs.String())
		}
	})
}

func TestPreloadMetadataSafeRetryAndPlaybackInfoNoRetry(t *testing.T) {
	for _, safe := range []bool{true, false} {
		t.Run(fmt.Sprint(safe), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				preloadLogger(t, &logs, helpers.LogLevelDebug)
				p := newTestPreloader(t)
				calls := 0
				var elapsed time.Duration
				p.Submit(preloadCandidate("metadata", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
					started := time.Now()
					err := task.Metadata(ctx, safe, func(context.Context) (bool, error) {
						calls++
						return true, errors.New("secret-url-must-not-be-logged")
					})
					if err == nil {
						t.Error("应保留元数据失败")
					}
					elapsed = time.Since(started)
					return PreloadResult{Outcome: PreloadSkipped}
				}))
				time.Sleep(time.Second)
				synctest.Wait()
				want, delay := 1, time.Duration(0)
				if safe {
					want, delay = 2, 400*time.Millisecond
				}
				if calls != want || elapsed != delay {
					t.Fatalf("safe=%v calls=%d elapsed=%v", safe, calls, elapsed)
				}
				if strings.Contains(logs.String(), "secret-url-must-not-be-logged") || strings.Count(logs.String(), "本轮额度内重试") != want-1 {
					t.Fatalf("安全重试诊断数量错误或泄漏：%s", logs.String())
				}
			})
		})
	}
}

func TestPreloadTakeoverNextKeepsSigningLeaderAndCancelsPendingRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		preloadLogger(t, &logs, helpers.LogLevelDebug)
		p := newTestPreloader(t)
		release := make(chan struct{})
		published := 0
		candidate := preloadCandidate("next-leader", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			if !task.BeginSignature(preloadTestAccount, "next-leader") {
				t.Error("应开始唯一签名")
			}
			select {
			case <-release:
			case <-ctx.Done():
				t.Error("切集接手不能取消已开始的共享签名")
			}
			task.Publish(func() { published++ })
			return PreloadResult{Outcome: PreloadPublished}
		})
		p.Submit(candidate)
		synctest.Wait()
		p.TakeoverNext("device")
		close(release)
		synctest.Wait()
		if published != 1 || p.Submit(candidate) || strings.Count(logs.String(), "已撤销预加载机会") != 1 {
			t.Fatalf("接手发布或撤销日志不符：published=%d logs=%s", published, logs.String())
		}
		pending := preloadCandidate("pending-next", PreloadNext, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
		p.Submit(pending)
		p.TakeoverNext("device")
		_, queued, _ := preloadSize(p)
		if queued != 0 {
			t.Fatal("未开始的下一项应由普通播放接手")
		}
	})
}

func TestPreloadTakeoverAndScopeCancellation(t *testing.T) {
	for _, phase := range []string{"queued", "metadata", "signing"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				release := make(chan struct{})
				started, signatures, published := 0, 0, 0
				candidate := preloadCandidate("takeover", PreloadDetail, func(ctx context.Context, task *PreloadTask) PreloadResult {
					started++
					if phase == "signing" && task.BeginSignature(preloadTestAccount, "takeover") {
						signatures++
					}
					select {
					case <-release:
					case <-ctx.Done():
					}
					if phase != "signing" && task.BeginSignature(preloadTestAccount, "takeover") {
						signatures++
					}
					task.Publish(func() { published++ })
					return PreloadResult{Outcome: PreloadPublished}
				})
				p.Submit(candidate)
				if phase != "queued" {
					time.Sleep(2 * time.Second)
					synctest.Wait()
				}
				p.Takeover("takeover")
				close(release)
				synctest.Wait()
				time.Sleep(3 * time.Second)
				synctest.Wait()
				want := 0
				if phase == "signing" {
					want = 1
				}
				if signatures != want || published != want || (phase == "queued" && started != 0) {
					t.Fatalf("接手阶段=%s，started=%d sign=%d pub=%d", phase, started, signatures, published)
				}
				next := preloadCandidate("distinct-next", PreloadNext, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
				if !p.Submit(next) {
					t.Fatal("当前项接手不能禁止不同下一项")
				}
			})
		})
	}
}

func TestPreloadCancelNextStopsSignatureAndKeepsCurrentDetail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		details, published := 0, 0
		started := make(chan struct{})
		next := preloadCandidate("canceled-next", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			if !task.BeginSignature(preloadTestAccount, "canceled-next") {
				t.Error("下一项应已开始唯一签名")
			}
			close(started)
			<-ctx.Done()
			if task.Publish(func() { published++ }) {
				t.Error("撤销进度后不能发布迟到签名")
			}
			return PreloadResult{Outcome: PreloadPublished}
		})
		p.Submit(next)
		<-started
		p.Submit(preloadCandidate("current-detail", PreloadDetail, func(context.Context, *PreloadTask) PreloadResult {
			details++
			return PreloadResult{Outcome: PreloadCached}
		}))
		p.CancelNext("device")
		synctest.Wait()
		if published != 0 || p.Submit(next) {
			t.Fatal("撤销后仍发布或重新接纳旧下一项")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if details != 1 {
			t.Fatalf("撤销下一项误取消当前详情：执行次数=%d", details)
		}
	})
}

func TestPreloadCancelRevokesCompletedRetry(t *testing.T) {
	for _, cancel := range []func(*Preloader){
		func(p *Preloader) { p.CancelScope("device") },
		func(p *Preloader) { p.CancelInstance("instance") },
		func(p *Preloader) { p.CancelAccount(preloadTestAccount) },
		func(p *Preloader) { p.TakeoverScope("device") },
	} {
		synctest.Test(t, func(t *testing.T) {
			p := newTestPreloader(t)
			candidate := preloadCandidate("retry-revoked", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				task.BeginSignature(preloadTestAccount, "retry-revoked")
				return PreloadResult{Outcome: PreloadTransient}
			})
			// TakeoverScope 只针对详情；其他入口同时覆盖下一项。
			candidate.Kind = PreloadDetail
			p.Submit(candidate)
			time.Sleep(2 * time.Second)
			synctest.Wait()
			cancel(p)
			time.Sleep(time.Minute)
			if p.Submit(candidate) {
				t.Fatal("取消必须撤销已完成失败的机会重试")
			}
		})
	}
}

func TestPreloadCancelAllPreservesAccountLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		riskAccount := PreloadAccount{Provider: "115", AccountID: 2, UserID: "risk-owner"}
		risk := preloadCandidate("risk", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			task.BeginSignature(riskAccount, "risk")
			return PreloadResult{Outcome: PreloadRisk}
		})
		risk.Account = riskAccount
		p.Submit(risk)
		synctest.Wait()
		var signatures, published, queued atomic.Int32
		old := preloadCandidate("old", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			if !task.BeginSignature(preloadTestAccount, "old") {
				t.Error("初始作业未进入签名")
				return PreloadResult{Outcome: PreloadSkipped}
			}
			signatures.Add(1)
			<-ctx.Done()
			task.Publish(func() { published.Add(1) })
			return PreloadResult{Outcome: PreloadTransient}
		})
		p.Submit(old)
		synctest.Wait()
		p.Submit(preloadCandidate("queued", PreloadDetail, func(context.Context, *PreloadTask) PreloadResult {
			queued.Add(1)
			return PreloadResult{Outcome: PreloadSkipped}
		}))
		p.CancelAll()
		synctest.Wait()
		if !p.Enabled() || p.settings.Load().generation != 1 || published.Load() != 0 || signatures.Load() != 1 || queued.Load() != 0 || p.Submit(old) {
			t.Fatal("整体撤销未阻止旧作业、重置次数或改变了开关代际")
		}
		risk.Key, risk.Source = "fresh-risk", "fresh-risk"
		if p.Submit(risk) {
			t.Fatal("整体撤销清除了账号风控冷却")
		}
		fresh := preloadCandidate("fresh", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			if task.BeginSignature(preloadTestAccount, "fresh") {
				signatures.Add(1)
				task.Publish(func() { published.Add(1) })
			}
			return PreloadResult{Outcome: PreloadPublished}
		})
		if !p.Submit(fresh) {
			t.Fatal("新候选被整体撤销永久阻止")
		}
		synctest.Wait()
		time.Sleep(9 * time.Second)
		synctest.Wait()
		if signatures.Load() != 1 || published.Load() != 0 || queued.Load() != 0 {
			t.Fatal("整体撤销绕过账号十秒间隔或恢复了旧排队作业")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if signatures.Load() != 2 || published.Load() != 1 {
			t.Fatal("配额恢复后新候选没有正常签名和发布")
		}
	})
}

func TestPreloadGenerationTimeoutAndCredentialCommit(t *testing.T) {
	for _, cancel := range []string{"settings", "credential", "instance", "timeout"} {
		t.Run(cancel, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				var elapsed time.Duration
				published, signatures := 0, 0
				p.Submit(preloadCandidate("lifecycle", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
					if !task.BindSource(PreloadAccount{Provider: "115"}, "lifecycle") {
						t.Error("旧格式源应能在凭据 SQL 前登记提供方")
					}
					start := time.Now()
					<-ctx.Done()
					elapsed = time.Since(start)
					if task.BindSource(preloadTestAccount, "lifecycle") && task.BeginSignature(preloadTestAccount, "lifecycle") {
						signatures++
					}
					task.Publish(func() { published++ })
					return PreloadResult{Outcome: PreloadPublished}
				}))
				synctest.Wait()
				switch cancel {
				case "settings":
					p.Apply(false, 2)
					p.Apply(true, 1)
					if p.Enabled() {
						t.Fatal("旧通知恢复了开关")
					}
				case "credential":
					p.CancelAccount(preloadTestAccount)
				case "instance":
					p.CancelInstance("instance")
				case "timeout":
					time.Sleep(3 * time.Second)
				}
				synctest.Wait()
				_, _, active := preloadSize(p)
				if signatures != 0 || published != 0 || active {
					t.Fatalf("失效后不应签名或发布：sign=%d pub=%d", signatures, published)
				}
				if cancel == "timeout" && elapsed != preloadRunLimit {
					t.Fatalf("执行预算=%v", elapsed)
				}
				p.Stop()
				p.Stop()
				states, queued, _ := preloadSize(p)
				if p.Enabled() || states+queued != 0 {
					t.Fatal("停止后残留状态或 worker")
				}
			})
		})
	}
}

func TestPreloadRiskCooldownAndSanitizedLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		var logs bytes.Buffer
		preloadLogger(t, &logs, helpers.LogLevelDebug)
		const secret = "sentinel-URL-token-cookie-native-body\nnew-line"
		calls := 0
		candidate := preloadCandidate(secret, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			calls++
			task.BeginSignature(preloadTestAccount, secret)
			return PreloadResult{Outcome: PreloadRisk, HTTPStatus: 429, RetryAfter: 15 * time.Minute}
		})
		candidate.Account = preloadTestAccount
		p.Submit(candidate)
		synctest.Wait()
		for range 100 {
			p.Submit(candidate)
		}
		other := candidate
		other.Key, other.Source, other.Scope, other.Enriched = "other", "other", "other-device", true
		p.Apply(false, 2)
		p.Apply(true, 3)
		if p.Submit(other) {
			t.Fatal("开关切换不能绕过账号风控")
		}
		time.Sleep(10 * time.Minute)
		if p.Submit(other) {
			t.Fatal("更长 Retry-After 应保留")
		}
		if calls != 1 || strings.Contains(logs.String(), secret) || strings.Count(logs.String(), "[WARN]") != 1 {
			t.Fatalf("冷却、日志去重或敏感输出错误：calls=%d logs=%s", calls, logs.String())
		}
		time.Sleep(5 * time.Minute)
		other.Run = func(context.Context, *PreloadTask) PreloadResult {
			calls++
			return PreloadResult{Outcome: PreloadCached}
		}
		if !p.Submit(other) {
			t.Fatal("冷却到期后新候选应可处理")
		}
		synctest.Wait()
		if calls != 2 {
			t.Fatal("冷却后未处理新候选")
		}
	})
}

func TestPreloadFreshEvidenceOnlyReopensMetadataSkip(t *testing.T) {
	for _, outcome := range []PreloadOutcome{PreloadSkipped, PreloadCached, PreloadRisk, PreloadUnauthorized, PreloadFailed} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				calls := 0
				candidate := preloadCandidate("evidence", PreloadNext, func(context.Context, *PreloadTask) PreloadResult { calls++; return PreloadResult{Outcome: outcome} })
				p.Submit(candidate)
				synctest.Wait()
				if p.Submit(candidate) {
					t.Fatal("相同资料不应重跑")
				}
				candidate.Enriched = true
				if actual := p.Submit(candidate); actual != (outcome == PreloadSkipped) {
					t.Fatalf("仅元数据跳过允许新证据：outcome=%v accepted=%v", outcome, actual)
				}
				synctest.Wait()
			})
		})
	}
}

type preloadBlockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *preloadBlockingWriter) Write(value []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(value), nil
}

func TestPreloadBlockingLoggerDoesNotHoldCoordinatorOrPublicationLocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := &preloadBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
		preloadLogger(t, writer, helpers.LogLevelInfo)
		p := newTestPreloader(t)
		var task *PreloadTask
		p.Submit(preloadCandidate("logging", PreloadNext, func(_ context.Context, current *PreloadTask) PreloadResult {
			task = current
			current.BeginSignature(preloadTestAccount, "logging")
			current.Publish(func() {})
			return PreloadResult{Outcome: PreloadPublished}
		}))
		<-writer.entered
		if !p.mu.TryLock() {
			t.Fatal("日志阻塞时仍持有全局队列锁")
		}
		p.mu.Unlock()
		if !task.gate.TryLock() {
			t.Fatal("日志阻塞时仍持有发布门禁")
		}
		task.gate.Unlock()
		p.Apply(false, 2)
		close(writer.release)
		synctest.Wait()
	})
}

func TestPreloadHotPathNeverSynchronouslyWritesCancellationLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := &preloadBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
		preloadLogger(t, writer, helpers.LogLevelDebug)
		p := newTestPreloader(t)
		candidate := func(key string) PreloadCandidate {
			return preloadCandidate(key, PreloadDetail, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
		}
		p.Submit(candidate("old"))
		var adapterLock sync.Mutex
		adapterLock.Lock()
		p.Submit(candidate("latest"))
		adapterLock.Unlock()
		<-writer.entered
		adapterLock.Lock()
		p.CancelDetail("device")
		p.TakeoverScope("device")
		p.CancelScope("device")
		adapterLock.Unlock()
		p.Apply(false, 2)
		close(writer.release)
		synctest.Wait()
	})
}

func TestPreloadQueueAgePriorityAndCombinedStateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		release := make(chan struct{})
		p.Submit(preloadCandidate("busy", PreloadNext, func(ctx context.Context, _ *PreloadTask) PreloadResult {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return PreloadResult{Outcome: PreloadCached}
		}))
		synctest.Wait()
		var order []string
		for _, kind := range []PreloadKind{PreloadNext, PreloadDetail} {
			key := fmt.Sprint(kind)
			candidate := preloadCandidate(key, kind, func(context.Context, *PreloadTask) PreloadResult {
				order = append(order, key)
				return PreloadResult{Outcome: PreloadCached}
			})
			candidate.Scope = key
			p.Submit(candidate)
		}
		time.Sleep(2 * time.Second)
		close(release)
		synctest.Wait()
		if len(order) != 2 || order[0] != fmt.Sprint(PreloadDetail) {
			t.Fatalf("就绪详情应先于下一项：%v", order)
		}
		for i := range 400 {
			key := fmt.Sprintf("capacity-%d", i)
			candidate := preloadCandidate(key, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				account := preloadTestAccount
				account.AccountID = uint(i + 10)
				if task.BeginSignature(account, key) {
					task.Publish(func() {})
				}
				return PreloadResult{Outcome: PreloadPublished}
			})
			candidate.Scope = key
			p.Submit(candidate)
			synctest.Wait()
			p.Takeover(key)
			if size, _, _ := preloadSize(p); size > preloadStateLimit {
				t.Fatalf("候选、账号、接手门禁的合计状态超限：%d", size)
			}
		}
	})
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		started := 0
		for i := range 20 {
			candidate := preloadCandidate(fmt.Sprint(i), PreloadNext, func(ctx context.Context, _ *PreloadTask) PreloadResult {
				started++
				<-ctx.Done()
				return PreloadResult{Outcome: PreloadSkipped}
			})
			candidate.Scope = candidate.Key
			p.Submit(candidate)
		}
		time.Sleep(16 * time.Second)
		synctest.Wait()
		_, queued, _ := preloadSize(p)
		if started != 5 || queued != 0 {
			t.Fatalf("过期队列不能继续执行：started=%d queue=%d", started, queued)
		}
	})
}

func TestPreloadPublicationAndSettingsAreLinearizedOutsideQueueLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		inPublish, release, applied := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var canceled <-chan struct{}
		published := 0
		p.Submit(preloadCandidate("publish", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			canceled = ctx.Done()
			task.BeginSignature(preloadTestAccount, "publish")
			task.Publish(func() {
				close(inPublish)
				<-release
				published++
			})
			if task.Publish(func() { published++ }) {
				t.Error("关闭设置后不能再次发布")
			}
			return PreloadResult{Outcome: PreloadPublished}
		}))
		<-inPublish
		go func() { p.Apply(false, 2); close(applied) }()
		<-canceled
		select {
		case <-applied:
			t.Fatal("关闭必须等待已获准的内存发布完成")
		default:
		}
		if !p.mu.TryLock() {
			t.Fatal("等待已有发布时不能持有队列锁")
		}
		p.mu.Unlock()
		close(release)
		<-applied
		synctest.Wait()
		if published != 1 || p.Enabled() {
			t.Fatalf("关闭线性化错误：published=%d enabled=%v", published, p.Enabled())
		}
	})
}

func TestPreloadMetadataRetryRespectsRemainingRunBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		calls := 0
		p.Submit(preloadCandidate("short-budget", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			started := time.Now()
			err := task.Metadata(ctx, true, func(context.Context) (bool, error) {
				calls++
				time.Sleep(2700 * time.Millisecond)
				return true, errors.New("temporary connection failure")
			})
			if err == nil || time.Since(started) != 2700*time.Millisecond {
				t.Error("不足 400ms 时应直接结束")
			}
			return PreloadResult{Outcome: PreloadSkipped}
		}))
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("剩余预算不足仍重试了元数据")
		}
	})
}

func TestPreloadWorkerAccountIsolation(t *testing.T) {
	for _, test := range []struct {
		name     string
		account  PreloadAccount
		parallel bool
	}{
		{"different UID", PreloadAccount{Provider: "115", AccountID: 2, UserID: "other-user"}, true},
		{"different provider", PreloadAccount{Provider: "baidu", AccountID: 2, UserID: "test-user"}, true},
		{"same account", preloadTestAccount, false},
		{"cloud account alias", PreloadAccount{Provider: "115", AccountID: 2, UserID: "test-user"}, false},
		{"same local account", PreloadAccount{Provider: "115", AccountID: 1, UserID: "other-user"}, false},
		{"unknown", PreloadAccount{}, false},
		{"provider only", PreloadAccount{Provider: "115"}, false},
		{"local ID only", PreloadAccount{Provider: "115", AccountID: 2}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				entered := make(chan string, 2)
				release := make(chan struct{})
				for i, account := range []PreloadAccount{preloadTestAccount, test.account} {
					key := fmt.Sprint(i)
					candidate := preloadCandidate(key, PreloadNext, func(ctx context.Context, _ *PreloadTask) PreloadResult {
						entered <- key
						select {
						case <-release:
						case <-ctx.Done():
						}
						return PreloadResult{Outcome: PreloadCached}
					})
					candidate.Account, candidate.Scope = account, key
					p.Submit(candidate)
					if i == 0 {
						<-entered
					}
				}
				synctest.Wait()
				want := 0
				if test.parallel {
					want = 1
				}
				if len(entered) != want {
					t.Fatalf("账号并发错误：第二作业开始=%d，期望=%d", len(entered), want)
				}
				close(release)
				synctest.Wait()
				if _, queued, active := preloadSize(p); queued != 0 || active {
					t.Fatal("释放后仍有在途或排队作业")
				}
			})
		})
	}
}

func TestPreloadBindingWakesDistinctAccountPastBlockedQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		bind, release := make(chan struct{}), make(chan struct{})
		entered := make(chan string, 4)
		unknown := preloadCandidate("unknown-first", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			entered <- "first"
			select {
			case <-bind:
			case <-ctx.Done():
				return PreloadResult{}
			}
			if !task.BindSource(preloadTestAccount, "unknown-first") {
				t.Error("未知账号应能绑定实际账号")
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return PreloadResult{Outcome: PreloadCached}
		})
		p.Submit(unknown)
		<-entered
		for _, key := range []string{"same", "unknown", "different", "third"} {
			candidate := preloadCandidate(key, PreloadNext, func(ctx context.Context, _ *PreloadTask) PreloadResult {
				entered <- key
				select {
				case <-release:
				case <-ctx.Done():
				}
				return PreloadResult{Outcome: PreloadCached}
			})
			candidate.Scope = key
			if key != "unknown" {
				candidate.Account = preloadTestAccount
			}
			if key == "different" || key == "third" {
				candidate.Account.AccountID = 2
				candidate.Account.UserID = key
			}
			p.Submit(candidate)
		}
		synctest.Wait()
		if len(entered) != 0 {
			t.Fatal("未绑定账号时不能冒险启动第二个作业")
		}
		close(bind)
		synctest.Wait()
		if len(entered) != 1 || <-entered != "different" {
			t.Fatal("明确的不同账号没有绕过同账号和未知账号等待项")
		}
		p.mu.Lock()
		active, queued := len(p.active), len(p.queue)
		p.mu.Unlock()
		if active != 2 || queued != 3 {
			t.Fatalf("双作业或队列限额错误：active=%d queued=%d", active, queued)
		}
		close(release)
		synctest.Wait()
		if _, queued, active := preloadSize(p); queued != 0 || active {
			t.Fatal("各账号释放后队列未排空")
		}
	})
}

func TestPreloadRefinedAliasRequeuesAndFreesWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		release := make(chan struct{})
		entered := make(chan string, 3)
		first := preloadCandidate("first", PreloadNext, func(ctx context.Context, _ *PreloadTask) PreloadResult {
			entered <- "first"
			select {
			case <-release:
			case <-ctx.Done():
			}
			return PreloadResult{Outcome: PreloadCached}
		})
		first.Account, first.Scope = preloadTestAccount, "first"
		p.Submit(first)
		<-entered
		bound := PreloadAccount{Provider: "115", AccountID: 1, UserID: "refined-user"}
		prepared, requests, providerCalls, runs := false, 0, 0, 0
		refined := preloadCandidate("refined", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			runs++
			if !prepared {
				task.Metadata(ctx, true, func(context.Context) (bool, error) { requests++; return false, nil })
				prepared = true
			}
			if !task.BindSource(bound, "refined") {
				return PreloadResult{Outcome: PreloadSkipped}
			}
			providerCalls++
			return PreloadResult{Outcome: PreloadCached}
		})
		refined.Account, refined.Scope = PreloadAccount{Provider: "115", UserID: "refined-user"}, "refined"
		p.Submit(refined)
		synctest.Wait()
		if runs != 1 || providerCalls != 0 {
			t.Fatalf("完整身份冲突后仍执行提供方：runs=%d provider=%d", runs, providerCalls)
		}
		third := preloadCandidate("third", PreloadNext, func(context.Context, *PreloadTask) PreloadResult {
			entered <- "third"
			return PreloadResult{Outcome: PreloadCached}
		})
		third.Account, third.Scope = PreloadAccount{Provider: "115", AccountID: 3, UserID: "third-user"}, "third"
		p.Submit(third)
		synctest.Wait()
		if len(entered) != 1 || <-entered != "third" {
			t.Fatal("绑定冲突的作业仍占住另一个账号的 worker")
		}
		close(release)
		synctest.Wait()
		if requests != 1 || providerCalls != 1 || runs != 2 {
			t.Fatalf("重入没有复用资料或发生重复调用：requests=%d provider=%d runs=%d", requests, providerCalls, runs)
		}
	})
}

func TestPreloadAliasUsesSameSignatureGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		starts := make(chan time.Time, 2)
		for i := range 2 {
			key := fmt.Sprint(i)
			account := preloadTestAccount
			account.AccountID += uint(i)
			candidate := preloadCandidate(key, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				if !task.BeginSignature(account, key) {
					t.Error("已知账号应在配额到期后开始签名")
				}
				starts <- time.Now()
				task.Publish(func() {})
				return PreloadResult{Outcome: PreloadPublished}
			})
			candidate.Account, candidate.Scope = account, key
			p.Submit(candidate)
			synctest.Wait()
		}
		if len(starts) != 1 {
			t.Fatal("不同本地账号不能绕过同一网盘用户的签名间隔")
		}
		first := <-starts
		time.Sleep(preloadAccountGap)
		synctest.Wait()
		if len(starts) != 1 || (<-starts).Sub(first) != preloadAccountGap {
			t.Fatal("账号别名的起始间隔错误")
		}
	})
}

func TestPreloadTwoWorkersCancelAndTakeover(t *testing.T) {
	for _, operation := range []string{"settings", "instance", "scope", "account", "takeover preparing", "takeover signing"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				release := make(chan struct{})
				entered := make(chan struct{}, 2)
				var published atomic.Int32
				accounts := []PreloadAccount{preloadTestAccount, {Provider: "115", AccountID: 2, UserID: "other-user"}}
				for i, account := range accounts {
					key := fmt.Sprint(i)
					candidate := preloadCandidate(key, PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
						if operation != "takeover preparing" && !task.BeginSignature(account, key) {
							t.Error("两个不同账号都应能签名")
						}
						entered <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
						}
						task.Publish(func() { published.Add(1) })
						return PreloadResult{Outcome: PreloadPublished}
					})
					candidate.Account, candidate.Scope = account, key
					p.Submit(candidate)
				}
				<-entered
				<-entered
				switch operation {
				case "settings":
					p.Apply(false, 2)
				case "instance":
					p.CancelInstance("instance")
				case "scope":
					p.CancelScope("0")
					p.CancelScope("1")
				case "account":
					p.CancelAccount(accounts[0])
					synctest.Wait()
					p.mu.Lock()
					remaining := len(p.active)
					p.mu.Unlock()
					if remaining != 1 {
						t.Fatal("取消一个账号误取消其他账号")
					}
					p.CancelAccount(accounts[1])
				default:
					p.Takeover("0")
					p.Takeover("1")
				}
				close(release)
				synctest.Wait()
				want := int32(0)
				if operation == "takeover signing" {
					want = 2
				}
				if _, queued, active := preloadSize(p); queued != 0 || active || published.Load() != want {
					t.Fatalf("双作业取消/接手错误：queue=%d active=%v published=%d", queued, active, published.Load())
				}
			})
		})
	}
}

func TestPreloadStopWaitsForBothWorkersAndPreventsLatePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		entered, exited := make(chan struct{}, 2), make(chan struct{}, 2)
		releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
		var published atomic.Int32
		for i, release := range releases {
			key := fmt.Sprint(i)
			account := PreloadAccount{Provider: "115", AccountID: uint(i + 1), UserID: key}
			candidate := preloadCandidate(key, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
				task.BeginSignature(account, key)
				entered <- struct{}{}
				<-release
				task.Publish(func() { published.Add(1) })
				exited <- struct{}{}
				return PreloadResult{Outcome: PreloadPublished}
			})
			candidate.Account, candidate.Scope = account, key
			p.Submit(candidate)
		}
		<-entered
		<-entered
		stopped := make(chan struct{})
		go func() { p.Stop(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("Stop 未等待两个执行回调退出")
		default:
		}
		close(releases[0])
		<-exited
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("Stop 只等待了一个 worker")
		default:
		}
		close(releases[1])
		<-stopped
		states, queued, active := preloadSize(p)
		if states != 0 || queued != 0 || active || p.Enabled() || published.Load() != 0 {
			t.Fatal("停止后仍有状态、在途作业或迟到发布")
		}
	})
}

func TestPreloadDeferredWorkersCannotOverflowSingleQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		entered, release := make(chan struct{}, 2), make(chan struct{})
		for i := range 2 {
			key := fmt.Sprint(i)
			account := PreloadAccount{Provider: "115", AccountID: uint(i + 1), UserID: key}
			candidate := preloadCandidate(key, PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				task.BeginSignature(account, key)
				return PreloadResult{Outcome: PreloadSkipped}
			})
			candidate.Account, candidate.Scope = account, key
			p.Submit(candidate)
		}
		<-entered
		<-entered
		for i := range preloadQueueLimit {
			key := fmt.Sprintf("queued-%d", i)
			candidate := preloadCandidate(key, PreloadDetail, func(context.Context, *PreloadTask) PreloadResult { return PreloadResult{Outcome: PreloadCached} })
			candidate.Scope = key
			if !p.Submit(candidate) {
				t.Fatal("未达到单队列容量就拒绝候选")
			}
		}
		p.mu.Lock()
		for _, job := range p.active {
			p.accounts[preloadAccountKey(preloadJobAccount(job))] = preloadAccountState{nextStart: time.Now().Add(preloadAccountGap)}
		}
		p.mu.Unlock()
		close(release)
		synctest.Wait()
		states, queued, active := preloadSize(p)
		if queued != preloadQueueLimit || active || states > preloadStateLimit {
			t.Fatalf("双作业退回时扩充了队列：states=%d queued=%d active=%v", states, queued, active)
		}
	})
}

func TestPreloadFinalMetadataGET(t *testing.T) {
	for _, test := range []struct {
		name       string
		firstFails bool
		retry      bool
		finalFails bool
	}{
		{name: "third final GET"},
		{name: "failed request cannot buy third GET", firstFails: true},
		{name: "retry cannot buy third GET", firstFails: true, retry: true},
		{name: "third GET never retries", finalFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				calls, finalCalls := 0, 0
				failure := errors.New("temporary metadata failure")
				p.Submit(preloadCandidate("final-get", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
					first := true
					ordinary := func(context.Context) (bool, error) {
						calls++
						if first && test.firstFails {
							first = false
							return test.retry, failure
						}
						first = false
						return false, nil
					}
					task.Metadata(ctx, true, ordinary)
					if calls == 1 {
						task.Metadata(ctx, true, ordinary)
					}
					if err := task.Metadata(ctx, true, ordinary); !errors.Is(err, ErrPreloadMetadataBudget) {
						t.Errorf("普通第三次应被拒绝：%v", err)
					}
					started := time.Now()
					err := task.FinalMetadataGET(ctx, func(context.Context) (bool, error) {
						finalCalls++
						if test.finalFails {
							return true, failure
						}
						return false, nil
					})
					switch {
					case test.firstFails && !errors.Is(err, ErrPreloadMetadataBudget):
						t.Errorf("此前失败或重试不能增加额度：%v", err)
					case !test.firstFails && test.finalFails && !errors.Is(err, failure):
						t.Errorf("最终 GET 应保留失败：%v", err)
					case !test.firstFails && !test.finalFails && err != nil:
						t.Errorf("明确最后 GET 应可执行：%v", err)
					}
					if time.Since(started) != 0 {
						t.Error("第三次 GET 不应退避重试")
					}
					if err := task.FinalMetadataGET(ctx, ordinary); !errors.Is(err, ErrPreloadMetadataBudget) {
						t.Errorf("不能再次申请最终 GET：%v", err)
					}
					if err := task.Metadata(ctx, false, ordinary); !errors.Is(err, ErrPreloadMetadataBudget) {
						t.Errorf("不能为 PlaybackInfo 或普通补查开第四次：%v", err)
					}
					return PreloadResult{Outcome: PreloadCached}
				}))
				time.Sleep(time.Second)
				synctest.Wait()
				want := 1
				if test.firstFails {
					want = 0
				}
				if calls != 2 || finalCalls != want {
					t.Fatalf("HTTP 次数错误：ordinary=%d final=%d", calls, finalCalls)
				}
			})
		})
	}
}

func TestPreloadFinalGETBeforeDefaultLimitKeepsSafeRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		calls := 0
		p.Submit(preloadCandidate("early-final", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			started := time.Now()
			err := task.FinalMetadataGET(ctx, func(context.Context) (bool, error) {
				calls++
				if calls == 1 {
					return true, errors.New("temporary failure")
				}
				return false, nil
			})
			if err != nil || time.Since(started) != 400*time.Millisecond {
				t.Errorf("前两次内的最后 GET 应保留原短重试：%v", err)
			}
			if err := task.FinalMetadataGET(ctx, func(context.Context) (bool, error) { calls++; return false, nil }); !errors.Is(err, ErrPreloadMetadataBudget) {
				t.Errorf("已完成最后 GET 后不能再获额外额度：%v", err)
			}
			return PreloadResult{Outcome: PreloadCached}
		}))
		time.Sleep(time.Second)
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("默认额度内的最后 GET 次数=%d", calls)
		}
	})
}

func TestPreloadFinalGETRetainsLifetimeBudgetOnNewSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		calls, signatures := 0, 0
		candidate := preloadCandidate("final-retry", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			call := func(context.Context) (bool, error) { calls++; return false, nil }
			if calls == 0 {
				task.Metadata(ctx, true, call)
				task.Metadata(ctx, true, call)
				if err := task.FinalMetadataGET(ctx, call); err != nil {
					t.Error(err)
				}
			} else {
				if err := task.Metadata(ctx, true, call); !errors.Is(err, ErrPreloadMetadataBudget) {
					t.Error("新信号重置了普通元数据额度")
				}
				if err := task.FinalMetadataGET(ctx, call); !errors.Is(err, ErrPreloadMetadataBudget) {
					t.Error("新信号重置了最终 GET 额度")
				}
			}
			if task.BeginSignature(preloadTestAccount, "final-retry") {
				signatures++
			}
			return PreloadResult{Outcome: PreloadTransient}
		})
		p.Submit(candidate)
		synctest.Wait()
		time.Sleep(preloadRetryWait)
		if !p.Submit(candidate) {
			t.Fatal("新信号应保留原有一次签名重入机会")
		}
		synctest.Wait()
		if calls != 3 || signatures != 2 {
			t.Fatalf("机会重入增加了元数据预算：meta=%d sign=%d", calls, signatures)
		}
	})
}

func TestPreloadFinalGETSharesDeadlineAndRejectsCanceledCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		calls := 0
		var elapsed time.Duration
		p.Submit(preloadCandidate("final-timeout", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			started := time.Now()
			for range 2 {
				task.Metadata(ctx, true, func(context.Context) (bool, error) { calls++; return false, nil })
			}
			time.Sleep(2900 * time.Millisecond)
			err := task.FinalMetadataGET(ctx, func(ctx context.Context) (bool, error) {
				calls++
				<-ctx.Done()
				return false, ctx.Err()
			})
			elapsed = time.Since(started)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("最终 GET 未共享作业期限：%v", err)
			}
			if err := task.FinalMetadataGET(ctx, func(context.Context) (bool, error) { calls++; return false, nil }); !errors.Is(err, ErrPreloadCanceled) {
				t.Errorf("超时后仍调用最终 GET：%v", err)
			}
			return PreloadResult{Outcome: PreloadSkipped}
		}))
		time.Sleep(preloadRunLimit)
		synctest.Wait()
		if calls != 3 || elapsed != preloadRunLimit {
			t.Fatalf("最终 GET 延长了执行期限：calls=%d elapsed=%v", calls, elapsed)
		}
	})
}

func TestPreloadRootCancelOrInternalFailureDrainsBothWorkers(t *testing.T) {
	for _, failure := range []string{"root canceled", "callback panic"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				preloadLogger(t, io.Discard, helpers.LogLevelDebug)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				p := NewPreloader()
				p.Start(ctx)
				p.Apply(true, 1)
				defer p.Stop()
				entered := make(chan struct{}, 2)
				panicNow := make(chan struct{})
				var published atomic.Int32
				for i := range 2 {
					key := fmt.Sprint(i)
					account := PreloadAccount{Provider: "115", AccountID: uint(i + 1), UserID: key}
					candidate := preloadCandidate(key, PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
						task.BeginSignature(account, key)
						entered <- struct{}{}
						if i == 0 && failure == "callback panic" {
							select {
							case <-panicNow:
								panic("test callback failure")
							case <-ctx.Done():
							}
						} else {
							<-ctx.Done()
						}
						task.Publish(func() { published.Add(1) })
						return PreloadResult{Outcome: PreloadPublished}
					})
					candidate.Account, candidate.Scope = account, key
					p.Submit(candidate)
				}
				<-entered
				<-entered
				if failure == "callback panic" {
					close(panicNow)
				} else {
					cancel()
				}
				<-p.done
				states, queued, active := preloadSize(p)
				if p.Enabled() || states != 0 || queued != 0 || active || published.Load() != 0 {
					t.Fatal("停止根上下文或内部异常后没有排空两个 worker")
				}
			})
		})
	}
}

func TestPreloadSourceBindingValidatesHintAndPreservesKnownIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		checked := make(chan struct{})
		candidate := preloadCandidate("hint", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			for _, wrong := range []PreloadAccount{
				{Provider: "baidu", AccountID: 1, UserID: preloadTestAccount.UserID},
				{Provider: "115", AccountID: 1, UserID: "other-user"},
				{Provider: "115", AccountID: 1},
			} {
				if task.BindSource(wrong, "hint") {
					t.Errorf("信任了与提示冲突或不完整的凭据身份：%+v", wrong)
				}
			}
			if !task.BindSource(preloadTestAccount, "hint") || !task.BindSource(PreloadAccount{Provider: "115"}, "hint") || !task.BeginSignature(preloadTestAccount, "hint") {
				t.Error("正常细化或重复部分绑定不应丢失已确认身份")
			}
			task.Publish(func() {})
			close(checked)
			return PreloadResult{Outcome: PreloadPublished}
		})
		candidate.Account = PreloadAccount{Provider: "115", UserID: preloadTestAccount.UserID}
		p.Submit(candidate)
		<-checked
		synctest.Wait()
	})
}

func TestPreloadMetadataBudgetSurvivesCanceledOrTimedOutPreparation(t *testing.T) {
	for _, operation := range []string{"canceled", "timed out"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newTestPreloader(t)
				prepared := make(chan struct{})
				calls, rounds := 0, 0
				candidate := preloadCandidate("retained-metadata", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
					rounds++
					call := func(context.Context) (bool, error) { calls++; return false, nil }
					if rounds == 1 {
						task.Metadata(ctx, true, call)
						task.Metadata(ctx, true, call)
						close(prepared)
						<-ctx.Done()
						if err := task.FinalMetadataGET(ctx, call); !errors.Is(err, ErrPreloadCanceled) {
							t.Errorf("已取消准备不应发起最终 GET：%v", err)
						}
						return PreloadResult{Outcome: PreloadSkipped}
					}
					if err := task.Metadata(ctx, true, call); !errors.Is(err, ErrPreloadMetadataBudget) {
						t.Errorf("取消或超时重置了普通请求额度：%v", err)
					}
					if err := task.FinalMetadataGET(ctx, call); err != nil {
						t.Errorf("新资料仍可完成最后一层 GET：%v", err)
					}
					return PreloadResult{Outcome: PreloadCached}
				})
				p.Submit(candidate)
				<-prepared
				if operation == "canceled" {
					p.CancelScope("device")
				} else {
					time.Sleep(preloadRunLimit)
				}
				synctest.Wait()
				candidate.Enriched = true
				if !p.Submit(candidate) {
					t.Fatal("新正常资料应能补齐未签名的准备任务")
				}
				synctest.Wait()
				if calls != 3 || rounds != 2 {
					t.Fatalf("准备重入扩大了额度：calls=%d rounds=%d", calls, rounds)
				}
			})
		})
	}
}

func TestPreloadFinalGETNeedsCompletedSuccessfulMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newTestPreloader(t)
		var calls atomic.Int32
		p.Submit(preloadCandidate("pending-metadata", PreloadNext, func(ctx context.Context, task *PreloadTask) PreloadResult {
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var requests sync.WaitGroup
			for range 2 {
				requests.Go(func() {
					task.Metadata(ctx, true, func(context.Context) (bool, error) {
						calls.Add(1)
						entered <- struct{}{}
						<-release
						return false, nil
					})
				})
			}
			<-entered
			<-entered
			final := func(context.Context) (bool, error) { calls.Add(1); return false, nil }
			if err := task.FinalMetadataGET(ctx, final); !errors.Is(err, ErrPreloadMetadataBudget) {
				t.Errorf("请求尚未成功完成就开放了第三次：%v", err)
			}
			close(release)
			requests.Wait()
			if err := task.FinalMetadataGET(ctx, final); err != nil {
				t.Errorf("前两次成功完成后未开放最终 GET：%v", err)
			}
			return PreloadResult{Outcome: PreloadCached}
		}))
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatalf("最终 GET 的成功前提或次数错误：%d", calls.Load())
		}
	})
}
