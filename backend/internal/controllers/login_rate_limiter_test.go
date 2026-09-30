package controllers

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func TestLoginRateLimiterLocksByIPAndUsername(t *testing.T) {
	limiter := NewLoginRateLimiter(3, time.Minute, time.Minute)
	ip := "127.0.0.1"
	username := "admin"

	for i := range 3 {
		if allowed, _ := limiter.Allow(ip, username); !allowed {
			t.Fatalf("第 %d 次失败前不应被锁定", i+1)
		}
		limiter.RecordFailure(ip, username, "password_mismatch")
	}
	if allowed, wait := limiter.Allow(ip, username); allowed || wait <= 0 {
		t.Fatalf("达到阈值后应被锁定，allowed=%v wait=%v", allowed, wait)
	}

	if allowed, _ := limiter.Allow(ip, "other"); !allowed {
		t.Fatalf("不同用户名不应共享锁定")
	}
	if allowed, _ := limiter.Allow("127.0.0.2", username); !allowed {
		t.Fatalf("不同 IP 不应共享锁定")
	}

	limiter.Reset(ip, username)
	if allowed, _ := limiter.Allow(ip, username); !allowed {
		t.Fatalf("Reset 后应允许登录")
	}
}

func TestLoginRateLimiterFailureLogUsesReadableChineseFields(t *testing.T) {
	var logBuf bytes.Buffer
	originalLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	t.Cleanup(func() {
		helpers.AppLogger = originalLogger
	})

	limiter := NewLoginRateLimiter(2, time.Minute, time.Minute)
	limiter.RecordFailure("127.0.0.1", "admin", "password_or_user_invalid")

	logText := logBuf.String()
	if !strings.Contains(logText, "reason=用户名或密码错误") {
		t.Fatalf("登录失败日志原因应使用中文，实际日志：%s", logText)
	}
	if !strings.Contains(logText, "locked_until=未锁定") {
		t.Fatalf("未锁定时 locked_until 应显示为未锁定，实际日志：%s", logText)
	}
	if strings.Contains(logText, "0001-01-01T00:00:00Z") {
		t.Fatalf("未锁定时不应输出零值时间，实际日志：%s", logText)
	}
}
