package controllers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/httpapi"

	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

const (
	loginFailureLimit  = 5
	loginFailureWindow = 15 * time.Minute
	loginLockDuration  = 15 * time.Minute
)

type loginRateLimitEntry struct {
	Count       int
	FirstSeen   time.Time
	LockedUntil time.Time
	LastReason  string
}

type LoginRateLimiter struct {
	mu           sync.Mutex
	limit        int
	window       time.Duration
	lockDuration time.Duration
	entries      map[string]*loginRateLimitEntry
}

func NewLoginRateLimiter(limit int, window time.Duration, lockDuration time.Duration) *LoginRateLimiter {
	return &LoginRateLimiter{
		limit:        limit,
		window:       window,
		lockDuration: lockDuration,
		entries:      make(map[string]*loginRateLimitEntry),
	}
}

var defaultLoginRateLimiter = NewLoginRateLimiter(loginFailureLimit, loginFailureWindow, loginLockDuration)

func loginRateLimitKey(ip string, username string) string {
	return strings.TrimSpace(ip) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func loginFailureReasonText(reason string) string {
	switch reason {
	case "password_or_user_invalid":
		return "用户名或密码错误"
	case "totp_invalid":
		return "动态验证码错误"
	default:
		if strings.TrimSpace(reason) == "" {
			return "未知原因"
		}
		return reason
	}
}

func loginLockedUntilText(lockedUntil time.Time) string {
	if lockedUntil.IsZero() {
		return "未锁定"
	}
	return lockedUntil.Format(time.RFC3339)
}

func (l *LoginRateLimiter) Allow(ip string, username string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := loginRateLimitKey(ip, username)
	entry, ok := l.entries[key]
	if !ok {
		return true, 0
	}
	if !entry.LockedUntil.IsZero() && now.Before(entry.LockedUntil) {
		return false, time.Until(entry.LockedUntil).Round(time.Second)
	}
	if now.Sub(entry.FirstSeen) > l.window {
		delete(l.entries, key)
		return true, 0
	}
	return true, 0
}

func (l *LoginRateLimiter) RecordFailure(ip string, username string, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := loginRateLimitKey(ip, username)
	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.FirstSeen) > l.window {
		entry = &loginRateLimitEntry{FirstSeen: now}
		l.entries[key] = entry
	}
	entry.Count++
	entry.LastReason = reason
	if entry.Count >= l.limit {
		entry.LockedUntil = now.Add(l.lockDuration)
	}
	if helpers.AppLogger != nil {
		helpers.AppLogger.Warnf(
			"登录失败：ip=%s username=%s reason=%s count=%d locked_until=%s",
			ip,
			username,
			loginFailureReasonText(reason),
			entry.Count,
			loginLockedUntilText(entry.LockedUntil),
		)
	}
}

func (l *LoginRateLimiter) Reset(ip string, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, loginRateLimitKey(ip, username))
}

func writeLoginRateLimited(c *gin.Context, wait time.Duration) {
	seconds := max(1, int((wait+time.Second-1)/time.Second))
	c.Header("Retry-After", strconv.Itoa(seconds))
	c.Header("Cache-Control", "no-store, private")
	httpapi.Failure(c, http.StatusTooManyRequests, httpapi.Error{Code: "RATE_LIMITED", Message: fmt.Sprintf("请求过于频繁，请 %d 秒后再试", seconds)})
}
