package open123

import (
	"sync"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	clientID := "test_client_id"
	clientSecret := "test_client_secret"

	client := NewClient(clientID, clientSecret)

	if client.clientID != clientID {
		t.Errorf("Expected clientID %s, got %s", clientID, client.clientID)
	}

	if client.clientSecret != clientSecret {
		t.Errorf("Expected clientSecret %s, got %s", clientSecret, client.clientSecret)
	}

	if client.baseURL != OPEN_BASE_URL {
		t.Errorf("Expected baseURL %s, got %s", OPEN_BASE_URL, client.baseURL)
	}

	if client.limiters == nil {
		t.Error("Expected limiters map to be initialized")
	}

	if client.refreshTokenChan == nil {
		t.Error("Expected refreshTokenChan to be initialized")
	}

	client.Close()
}

func TestInitDefaultRateLimits(t *testing.T) {
	client := NewClient("test_id", "test_secret")
	client.initDefaultRateLimits()

	if client.limiters["/api/v1/"] == nil {
		t.Error("Expected /api/v1/ rate limiter to be initialized")
	}

	if client.limiters["/upload/v2/"] == nil {
		t.Error("Expected /upload/v2/ rate limiter to be initialized")
	}

	if client.limiters["/api/v2/"] == nil {
		t.Error("Expected /api/v2/ rate limiter to be initialized")
	}

	client.Close()
}

func TestSetRateLimit(t *testing.T) {
	client := NewClient("test_id", "test_secret")

	client.SetRateLimit("/test/path", 5)

	if client.limiters["/test/path"] == nil {
		t.Error("Expected /test/path rate limiter to be set")
	}

	client.Close()
}

func TestIsTokenExpired(t *testing.T) {
	client := NewClient("test_id", "test_secret")

	client.expiredAt = time.Now().Add(-1 * time.Hour)
	if !client.isTokenExpired() {
		t.Error("Expected token to be expired")
	}

	client.expiredAt = time.Now().Add(1 * time.Hour)
	if client.isTokenExpired() {
		t.Error("Expected token not to be expired")
	}

	client.Close()
}

func TestGetAccessToken(t *testing.T) {
	client := NewClient("test_id", "test_secret")

	client.tokenMu.Lock()
	client.accessToken = "test_token"
	client.tokenMu.Unlock()

	token := client.GetAccessToken()
	if token != "test_token" {
		t.Errorf("Expected token %s, got %s", "test_token", token)
	}

	client.Close()
}

func TestGetExpiredAt(t *testing.T) {
	client := NewClient("test_id", "test_secret")

	expectedTime := time.Now()

	client.tokenMu.Lock()
	client.expiredAt = expectedTime
	client.tokenMu.Unlock()

	expiredAt := client.GetExpiredAt()
	if expiredAt != expectedTime {
		t.Errorf("Expected expiredAt %v, got %v", expectedTime, expiredAt)
	}

	client.Close()
}

func TestNewAPIError(t *testing.T) {
	code := 401
	message := "Unauthorized"

	err := NewAPIError(code, message)

	if err.Code != code {
		t.Errorf("Expected code %d, got %d", code, err.Code)
	}

	if err.Message != message {
		t.Errorf("Expected message %s, got %s", message, err.Message)
	}
}

func TestIsTokenExpiredFunc(t *testing.T) {
	tokenExpiredErr := NewAPIError(ErrCodeUnauthorized, "Token expired")
	if !IsTokenExpired(tokenExpiredErr) {
		t.Error("Expected token expired error to be detected")
	}

	otherErr := NewAPIError(ErrCodeNotFound, "Not found")
	if IsTokenExpired(otherErr) {
		t.Error("Expected other error not to be detected as token expired")
	}
}

func TestIsRateLimited(t *testing.T) {
	rateLimitErr := NewAPIError(ErrCodeRateLimit, "Rate limit exceeded")
	if !IsRateLimited(rateLimitErr) {
		t.Error("Expected rate limit error to be detected")
	}

	otherErr := NewAPIError(ErrCodeNotFound, "Not found")
	if IsRateLimited(otherErr) {
		t.Error("Expected other error not to be detected as rate limited")
	}
}

func TestConcurrentTokenAccess(t *testing.T) {
	client := NewClient("test_id", "test_secret")

	client.tokenMu.Lock()
	client.accessToken = "test_token"
	client.tokenMu.Unlock()

	var wg sync.WaitGroup
	tokenCounts := make(map[string]int)
	var mu sync.Mutex

	for range 100 {
		wg.Go(func() {
			token := client.GetAccessToken()
			mu.Lock()
			tokenCounts[token]++
			mu.Unlock()
		})
	}

	wg.Wait()

	if len(tokenCounts) != 1 {
		t.Errorf("Expected all tokens to be the same, got %d different values", len(tokenCounts))
	}

	if tokenCounts["test_token"] != 100 {
		t.Errorf("Expected 100 reads of 'test_token', got %d", tokenCounts["test_token"])
	}

	client.Close()
}

func TestFileUploadCreateRequest(t *testing.T) {
	req := &FileUploadCreateRequest{
		ParentFileID: 0,
		Filename:     "测试文件.txt",
		Etag:         "0a05e3dcd8ba1d14753597bc8611d0a1",
		Size:         44321,
	}

	if req.ParentFileID != 0 {
		t.Errorf("Expected ParentFileID 0, got %d", req.ParentFileID)
	}

	if req.Filename != "测试文件.txt" {
		t.Errorf("Expected Filename '测试文件.txt', got '%s'", req.Filename)
	}

	if req.Etag != "0a05e3dcd8ba1d14753597bc8611d0a1" {
		t.Errorf("Expected Etag '0a05e3dcd8ba1d14753597bc8611d0a1', got '%s'", req.Etag)
	}

	if req.Size != 44321 {
		t.Errorf("Expected Size 44321, got %d", req.Size)
	}
}
