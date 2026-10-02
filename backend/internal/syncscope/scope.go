// Package syncscope 让会改动同一批 STRM 文件或记录的任务按顺序执行。
package syncscope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Scope 描述任务可能读写的同步目录、远端路径和本地路径。
// 同一目录 ID、重叠的远端目录或本地路径都会等待。
// 远端来源或账号缺失时，保守按可能相同处理。
type Scope struct {
	SyncPathID uint
	// SharedConfig 只共享目录配置保护；本地和远端位置仍然排他。
	SharedConfig bool
	SourceType   string
	AccountID    uint
	RemotePath   string
	LocalPath    string
	// WholeAccount 用于只知道账号、无法确定具体目录的任务。
	WholeAccount bool
	// Global 用于无法确认影响范围的任务，会等待所有其他任务。
	Global bool
}

var shared coordinator

// Acquire 一次申请本次任务的全部范围，直到相关任务结束或 ctx 取消。
// 必须在读取旧记录前申请；不要持有范围再申请，也不要在 SQL 事务中等待。
// 成功后由调用方执行 release；可以移交后台，重复调用不会提前释放后续任务。
// ctx 只取消等待，申请成功后不会自动释放，避免后台仍在写入时放行下一任务。
// 空范围按 Global 处理。本地路径会解析符号链接，尚未创建的部分按已有父目录定位。
func Acquire(ctx context.Context, scopes ...Scope) (release func(), err error) {
	release, _, err = shared.acquireObserved(ctx, scopes...)
	return release, err
}

// AcquireObserved 同时返回申请是否曾因其他任务而等待。
func AcquireObserved(ctx context.Context, scopes ...Scope) (func(), bool, error) {
	return shared.acquireObserved(ctx, scopes...)
}

type request struct {
	scopes  []Scope
	ready   chan struct{}
	granted bool
}

type coordinator struct {
	mu       sync.Mutex
	requests []*request
}

func (c *coordinator) acquire(ctx context.Context, scopes ...Scope) (func(), error) {
	release, _, err := c.acquireObserved(ctx, scopes...)
	return release, err
}

func (c *coordinator) acquireObserved(ctx context.Context, scopes ...Scope) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if len(scopes) == 0 {
		scopes = []Scope{{Global: true}}
	}
	r := &request{scopes: slices.Clone(scopes), ready: make(chan struct{})}
	for i, scope := range r.scopes {
		normalized, err := normalize(scope)
		if err != nil {
			return nil, false, err
		}
		r.scopes[i] = normalized
	}
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.grant()
	waited := !r.granted
	c.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-r.ready:
	}
	// 取消和放行可能同时发生，两种情况都要撤回这次申请。
	if err := ctx.Err(); err != nil {
		c.remove(r)
		return nil, false, err
	}
	return sync.OnceFunc(func() { c.remove(r) }), waited, nil
}

func (c *coordinator) remove(r *request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.requests, r); i >= 0 {
		c.requests = slices.Delete(c.requests, i, i+1)
	}
	c.grant()
}

func (c *coordinator) grant() {
	// ponytail: 按任务和范围两两比较；任务或旧目录很多时再按目录建索引。
	for i, r := range c.requests {
		if r.granted {
			continue
		}
		blocked := false
		for j, other := range c.requests {
			if i != j && (other.granted || j < i) && overlap(r.scopes, other.scopes) {
				blocked = true
				break
			}
		}
		if !blocked {
			r.granted = true
			close(r.ready)
		}
	}
}

func normalize(s Scope) (Scope, error) {
	if s.Global || (s.SyncPathID == 0 && s.RemotePath == "" && s.LocalPath == "" && !s.WholeAccount) ||
		((s.RemotePath != "" || s.WholeAccount) && s.SourceType == "" && s.AccountID == 0) {
		return Scope{Global: true}, nil
	}
	if s.RemotePath != "" {
		s.RemotePath = path.Clean("/" + s.RemotePath)
	}
	if s.LocalPath != "" {
		local, err := ResolveLocalPath(s.LocalPath)
		if err != nil {
			return Scope{}, fmt.Errorf("无法确定 STRM 本地路径: %w", err)
		}
		s.LocalPath = filepath.ToSlash(local)
	}
	return s, nil
}

// ResolveLocalPath 解析本地符号链接；未创建的部分按最近已有父目录定位。
func ResolveLocalPath(local string) (string, error) {
	local, err := filepath.Abs(local)
	if err != nil {
		return "", err
	}
	tail := ""
	for {
		resolved, err := filepath.EvalSymlinks(local)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// 路径可能刚被其他任务创建，重查后再判断；断链仍不能当普通新目录。
		if _, statErr := os.Lstat(local); !errors.Is(statErr, os.ErrNotExist) {
			if statErr != nil {
				return "", statErr
			}
			resolved, err = filepath.EvalSymlinks(local)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, tail), nil
		}
		parent := filepath.Dir(local)
		if parent == local {
			return "", err
		}
		tail = filepath.Join(filepath.Base(local), tail)
		local = parent
	}
}

// Overlap 判断两组范围是否可能影响同一位置，并解析本地符号链接。
func Overlap(left, right []Scope) (bool, error) {
	normalizeAll := func(scopes []Scope) ([]Scope, error) {
		result := make([]Scope, len(scopes))
		for i, scope := range scopes {
			var err error
			result[i], err = normalize(scope)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	a, err := normalizeAll(left)
	if err != nil {
		return false, err
	}
	b, err := normalizeAll(right)
	if err != nil {
		return false, err
	}
	return overlap(a, b), nil
}

func overlap(left, right []Scope) bool {
	for _, a := range left {
		for _, b := range right {
			if a.Global || b.Global || (a.SyncPathID != 0 && a.SyncPathID == b.SyncPathID && !(a.SharedConfig && b.SharedConfig)) ||
				pathsOverlap(a.LocalPath, b.LocalPath) {
				return true
			}
			if (a.SourceType == "" && a.AccountID == 0) || (b.SourceType == "" && b.AccountID == 0) {
				continue
			}
			if a.SourceType != "" && b.SourceType != "" && a.SourceType != b.SourceType {
				continue
			}
			if a.AccountID != 0 && b.AccountID != 0 && a.AccountID != b.AccountID {
				continue
			}
			if a.WholeAccount || b.WholeAccount || pathsOverlap(a.RemotePath, b.RemotePath) {
				return true
			}
		}
	}
	return false
}

func pathsOverlap(a, b string) bool {
	return a != "" && b != "" && (a == b ||
		strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") ||
		strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/"))
}
