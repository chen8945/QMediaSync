package syncscope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
)

func TestAcquireScopeOverlap(t *testing.T) {
	remote := func(source string, account uint, value string) Scope {
		return Scope{SourceType: source, AccountID: account, RemotePath: value}
	}
	cases := []struct {
		name  string
		first Scope
		next  Scope
		wait  bool
	}{
		{"同一保存目录", Scope{SyncPathID: 1}, Scope{SyncPathID: 1}, true},
		{"不同保存目录", Scope{SyncPathID: 1}, Scope{SyncPathID: 2}, false},
		{"远端父子目录", remote("115", 1, "/a"), remote("115", 1, "/a/b"), true},
		{"远端子父目录", remote("115", 1, "/a/b"), remote("115", 1, "/a"), true},
		{"远端名称前缀不同目录", remote("115", 1, "/a"), remote("115", 1, "/ab"), false},
		{"远端根目录", remote("115", 1, "/"), remote("115", 1, "/a"), true},
		{"远端路径整理", remote("115", 1, "/a/b/.."), remote("115", 1, "a/./c"), true},
		{"不同账号相同远端目录", remote("115", 1, "/a"), remote("115", 2, "/a"), false},
		{"不同来源相同账号数字", remote("115", 1, "/a"), remote("baidupan", 1, "/a"), false},
		{"来源未知保守等待", remote("", 1, "/a"), remote("115", 1, "/a/b"), true},
		{"账号未知保守等待", remote("115", 0, "/a"), remote("115", 2, "/a/b"), true},
		{"同账号独立远端目录", remote("115", 1, "/a"), remote("115", 1, "/b"), false},
		{"缺少路径不会自动锁整个账号", Scope{SyncPathID: 1, SourceType: "115", AccountID: 1}, Scope{SyncPathID: 2, SourceType: "115", AccountID: 1}, false},
		{"整个账号", Scope{SourceType: "115", AccountID: 1, WholeAccount: true}, remote("115", 1, "/a"), true},
		{"整个账号匹配只知道目录ID的任务", Scope{SourceType: "115", AccountID: 1, WholeAccount: true}, Scope{SyncPathID: 2, SourceType: "115", AccountID: 1}, true},
		{"整个账号不影响其他账号", Scope{SourceType: "115", AccountID: 1, WholeAccount: true}, remote("115", 2, "/a"), false},
		{"账号来源未知", Scope{AccountID: 1, WholeAccount: true}, remote("115", 1, "/a"), true},
		{"来源内账号未知", Scope{SourceType: "115", WholeAccount: true}, remote("115", 2, "/a"), true},
		{"跨账号相同本地目标", Scope{SourceType: "115", AccountID: 1, LocalPath: "/media/a"}, Scope{SourceType: "115", AccountID: 2, LocalPath: "/media/a"}, true},
		{"本地父子目录", Scope{LocalPath: "/media/a"}, Scope{LocalPath: "/media/a/b"}, true},
		{"本地名称前缀不同目录", Scope{LocalPath: "/media/a"}, Scope{LocalPath: "/media/ab"}, false},
		{"本地相对路径", Scope{LocalPath: "media/a"}, Scope{LocalPath: filepath.Join(mustWorkingPath(t), "media/a/b")}, true},
		{"显式全局", Scope{Global: true}, remote("115", 1, "/a"), true},
		{"空范围保守等待", Scope{}, Scope{LocalPath: "/media/a"}, true},
		{"只有账号未声明范围", Scope{AccountID: 1}, Scope{LocalPath: "/media/a"}, true},
		{"只有远端路径归属未知", Scope{RemotePath: "/a"}, Scope{LocalPath: "/media/a"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var c coordinator
				release, err := c.acquire(t.Context(), tc.first)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				next := startAcquire(t.Context(), &c, tc.next)
				synctest.Wait()
				if tc.wait {
					assertWaiting(t, next)
				}
				if !tc.wait {
					select {
					case result := <-next:
						requireRelease(t, result)()
					default:
						t.Fatal("独立目录不应等待")
					}
					return
				}
				release()
				requireRelease(t, <-next)()
			})
		})
	}
}

func TestAcquireConflictOrderAndIndependentScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c coordinator
		release, err := c.acquire(t.Context(), Scope{LocalPath: "/a/first"})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		parent := startAcquire(t.Context(), &c, Scope{LocalPath: "/a"})
		synctest.Wait()
		later := startAcquire(t.Context(), &c, Scope{LocalPath: "/a/next"})
		independent := startAcquire(t.Context(), &c, Scope{LocalPath: "/b"})
		requireRelease(t, <-independent)()
		synctest.Wait()
		assertWaiting(t, parent)
		assertWaiting(t, later)
		release()
		finishParent := requireRelease(t, <-parent)
		synctest.Wait()
		assertWaiting(t, later)
		finishParent()
		requireRelease(t, <-later)()
	})
}

func TestAcquireWholeGroupAndQueuedCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c coordinator
		a, b := Scope{LocalPath: "/a"}, Scope{LocalPath: "/b"}
		release, err := c.acquire(t.Context(), a, b)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		reversed := startAcquire(t.Context(), &c, b, a)
		synctest.Wait()
		assertWaiting(t, reversed)
		release()
		finishReversed := requireRelease(t, <-reversed)
		finishReversed()

		result, err := c.acquire(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		defer result()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		group := startAcquire(ctx, &c, a, b)
		synctest.Wait()
		later := startAcquire(t.Context(), &c, b)
		synctest.Wait()
		assertWaiting(t, later)
		cancel()
		canceled := <-group
		if !errors.Is(canceled.err, context.Canceled) || canceled.release != nil {
			t.Fatalf("等待取消结果 = %#v", canceled)
		}
		requireRelease(t, <-later)()
	})
}

func TestAcquireBackgroundHandoffAndRepeatedRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		release, err := Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		cancel()
		finishBackground := make(chan struct{})
		go func() {
			<-finishBackground
			release()
		}()
		next := startAcquire(t.Context(), &shared, Scope{SyncPathID: 1})
		synctest.Wait()
		assertWaiting(t, next)
		close(finishBackground)
		finishNext := requireRelease(t, <-next)
		last := startAcquire(t.Context(), &shared, Scope{SyncPathID: 1})
		release()
		synctest.Wait()
		assertWaiting(t, last)
		finishNext()
		requireRelease(t, <-last)()
	})
}

func TestAcquireCancellationRacingWithRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c coordinator
		for range 100 {
			release, err := c.acquire(t.Context(), Scope{SyncPathID: 1})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			next := startAcquire(ctx, &c, Scope{SyncPathID: 1})
			synctest.Wait()
			var wg sync.WaitGroup
			wg.Go(release)
			wg.Go(cancel)
			wg.Wait()
			result := <-next
			if result.err == nil {
				requireRelease(t, result)()
			} else if !errors.Is(result.err, context.Canceled) || result.release != nil {
				t.Fatalf("竞争后的结果 = %#v", result)
			}
			if release, err := c.acquire(ctx, Scope{SyncPathID: 1}); !errors.Is(err, context.Canceled) || release != nil {
				t.Fatal("已取消的申请不能成功")
			}
		}
		requireRelease(t, <-startAcquire(t.Context(), &c, Scope{SyncPathID: 1}))()
	})
}

func TestAcquireLocalSymlinks(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	if err := os.MkdirAll(filepath.Join(actual, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": "actual", "chained": "alias"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alias", "chained"} {
		for _, tail := range []string{"existing", "existing/movie.strm", "new/season/movie.strm"} {
			t.Run(name+"/"+tail, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var c coordinator
					release, err := c.acquire(t.Context(), Scope{LocalPath: filepath.Join(actual, tail)})
					if err != nil {
						t.Fatal(err)
					}
					defer release()
					next := startAcquire(t.Context(), &c, Scope{LocalPath: filepath.Join(root, name, tail)})
					synctest.Wait()
					assertWaiting(t, next)
					release()
					requireRelease(t, <-next)()
				})
			})
		}
	}
}

func TestAcquireRejectsUnresolvableLocalPath(t *testing.T) {
	root := t.TempDir()
	for name, target := range map[string]string{"loop": "loop", "broken": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"loop", "loop/movie.strm", "broken", "broken/new/movie.strm", "file/child"} {
		t.Run(name, func(t *testing.T) {
			var c coordinator
			release, err := c.acquire(t.Context(), Scope{LocalPath: filepath.Join(root, name)})
			if release != nil {
				release()
			}
			if err == nil || release != nil {
				t.Fatal("无法确定本地路径时不能继续处理")
			}
			release, err = c.acquire(t.Context(), Scope{Global: true})
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestResolveLocalPathDuringDirectoryCreation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 同目录的文件并行生成时，目录可能在解析路径的两次检查之间创建。
	for i := range 20000 {
		dir := filepath.Join(root, fmt.Sprintf("d%d", i))
		target := filepath.Join(dir, "movie.strm")
		ready := make(chan struct{})
		var wg sync.WaitGroup
		var mkdirErr, resolveErr error
		var resolved string
		wg.Go(func() {
			<-ready
			mkdirErr = os.MkdirAll(dir, 0o755)
		})
		wg.Go(func() {
			<-ready
			resolved, resolveErr = ResolveLocalPath(target)
		})
		close(ready)
		wg.Wait()
		if mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if resolveErr != nil || resolved != target {
			t.Fatalf("目录创建前后都应正确解析新文件：iteration=%d resolved=%q err=%v", i, resolved, resolveErr)
		}
	}
}

type acquisition struct {
	release func()
	err     error
}

func startAcquire(ctx context.Context, c *coordinator, scopes ...Scope) <-chan acquisition {
	result := make(chan acquisition, 1)
	go func() {
		release, err := c.acquire(ctx, scopes...)
		result <- acquisition{release: release, err: err}
	}()
	return result
}

func assertWaiting(t *testing.T, result <-chan acquisition) {
	t.Helper()
	select {
	case got := <-result:
		if got.release != nil {
			got.release()
		}
		t.Fatalf("任务应等待，实际结果 = %#v", got)
	default:
	}
}

func requireRelease(t *testing.T, result acquisition) func() {
	t.Helper()
	if result.err != nil || result.release == nil {
		t.Fatalf("申请失败: %v", result.err)
	}
	return result.release
}

func mustWorkingPath(t *testing.T) string {
	t.Helper()
	value, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSharedConfigKeepsTargetsExclusive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c coordinator
		root := t.TempDir()
		first, err := c.acquire(t.Context(), Scope{SyncPathID: 1, SharedConfig: true}, Scope{LocalPath: filepath.Join(root, "a.strm")})
		if err != nil {
			t.Fatal(err)
		}
		defer first()
		second, waited, err := c.acquireObserved(t.Context(), Scope{SyncPathID: 1, SharedConfig: true}, Scope{LocalPath: filepath.Join(root, "b.strm")})
		if err != nil || waited {
			t.Fatalf("独立输出不应等待：%v %v", waited, err)
		}
		defer second()
		for _, blocked := range []Scope{{SyncPathID: 1}, {LocalPath: filepath.Join(root, "a.strm")}, {LocalPath: root}} {
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				release, err := c.acquire(ctx, blocked)
				if release != nil {
					release()
				}
				done <- err
			}()
			synctest.Wait()
			select {
			case err := <-done:
				t.Fatalf("重叠范围提前完成：%v", err)
			default:
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		}
	})
}
