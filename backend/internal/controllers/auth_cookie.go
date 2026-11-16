package controllers

import (
	"fmt"
	"net/http"
	"time"

	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	authCookieName                 = "auth_token"
	csrfCookieName                 = "csrf_token"
	csrfHeaderName                 = "X-CSRF-Token"
	apiKeyHeaderName               = "X-API-Key"
	sessionAuthenticationChallenge = `QMediaSyncSession realm="QMediaSync"`
	apiKeyAuthenticationChallenge  = `QMediaSyncApiKey realm="QMediaSync"`
)

type LoginUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

type SessionClaimsInput struct {
	UserID    uint
	Username  string
	SessionID string
	TokenID   string
	ExpiresAt int64
}

func SignSessionJWT(input SessionClaimsInput) (string, error) {
	claims := &LoginUser{
		ID:        input.UserID,
		Username:  input.Username,
		SessionID: input.SessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        input.TokenID,
			ExpiresAt: jwt.NewNumericDate(time.Unix(input.ExpiresAt, 0)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(helpers.GlobalConfig.JwtSecret))
}

// ValidateJWT 校验 Cookie 中的登录会话票据。
func ValidateJWT(tokenString string) (*LoginUser, error) {
	token, err := jwt.ParseWithClaims(tokenString, &LoginUser{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("登录凭证签名算法不正确")
		}
		return []byte(helpers.GlobalConfig.JwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("登录凭证校验失败：%v", err)
	}
	claims, ok := token.Claims.(*LoginUser)
	if !ok || claims.ID == 0 || claims.Username == "" || claims.SessionID == "" || claims.RegisteredClaims.ID == "" {
		return nil, fmt.Errorf("登录凭证内容不完整")
	}
	return claims, nil
}

func isSecureCookie(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return c.Request.Header.Get("X-Forwarded-Proto") == "https"
}

func setSessionCookies(c *gin.Context, tokenString string, csrfToken string, maxAge int) {
	secure := isSecureCookie(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     authCookieName,
		Value:    tokenString,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	setCSRFCookie(c, csrfToken, maxAge)
}

func setCSRFCookie(c *gin.Context, csrfToken string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   isSecureCookie(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookies(c *gin.Context) {
	secure := isSecureCookie(c)
	for _, name := range []string{authCookieName, csrfCookieName} {
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == authCookieName,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func setAuthenticationChallenge(c *gin.Context) {
	challenge := sessionAuthenticationChallenge
	if CurrentAuthMethod(c) == authMethodAPIKey || apiKeyFromRequest(c) != "" {
		challenge = apiKeyAuthenticationChallenge
	}
	c.Header("WWW-Authenticate", challenge)
	c.Header("Cache-Control", "no-store, private")
}
