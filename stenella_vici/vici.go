package stenella_vici

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const (
	UserIDKey          contextKey = "vici_user_id"
	IsAuthenticatedKey contextKey = "vici_is_authenticated"
	UserMetadataKey    contextKey = "vici_metadata"
)

type Config struct {
	CookieName    string
	CookieMaxAge  int
	AuthHeaderKey string
	AuthParamKey  string
	SecretKey     []byte
	EnableLogging bool
}

func DefaultConfig() Config {
	return Config{
		CookieName:    "vici_session",
		CookieMaxAge:  86400,
		AuthHeaderKey: "Authorization",
		AuthParamKey:  "auth_token",
		EnableLogging: true,
	}
}

func Middleware(cfg Config, next http.Handler) http.Handler {
	if cfg.CookieName == "" {
		cfg.CookieName = "vici_session"
	}
	if cfg.AuthHeaderKey == "" {
		cfg.AuthHeaderKey = "Authorization"
	}
	if cfg.AuthParamKey == "" {
		cfg.AuthParamKey = "auth_token"
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		userID, isAuthenticated, metadata := extractUserContext(r, cfg)

		ctx = context.WithValue(ctx, UserIDKey, userID)
		ctx = context.WithValue(ctx, IsAuthenticatedKey, isAuthenticated)
		ctx = context.WithValue(ctx, UserMetadataKey, metadata)

		if !isAuthenticated && userID != "" {
			cookie, err := r.Cookie(cfg.CookieName)
			if err != nil || cookie.Value != userID {
				http.SetCookie(w, &http.Cookie{
					Name:     cfg.CookieName,
					Value:    userID,
					Path:     "/",
					HttpOnly: true,
					Secure:   false,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   cfg.CookieMaxAge,
				})
			}
		}

		if cfg.EnableLogging {
			logSessionEvent(r, userID, isAuthenticated)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractUserContext(r *http.Request, cfg Config) (userID string, isAuthenticated bool, metadata map[string]string) {
	metadata = make(map[string]string)

	authHeader := r.Header.Get(cfg.AuthHeaderKey)
	if authHeader != "" && isValidToken(authHeader, cfg.SecretKey) {
		isAuthenticated = true
		userID = "auth_" + generateShortID()
		metadata["auth_method"] = "header"
		return
	}

	authParam := r.URL.Query().Get(cfg.AuthParamKey)
	if authParam != "" && isValidToken(authParam, cfg.SecretKey) {
		isAuthenticated = true
		userID = "auth_" + generateShortID()
		metadata["auth_method"] = "url_param"
		return
	}

	cookie, err := r.Cookie(cfg.CookieName)
	if err == nil && cookie.Value != "" {
		userID = cookie.Value
	} else {
		userID = generateSessionID()
	}

	return userID, false, metadata
}

func isValidToken(token string, secret []byte) bool {
	if len(secret) == 0 {
		return false
	}
	parts := strings.SplitN(token, "_", 2)
	if len(parts) != 2 || parts[0] != "valid" {
		return false
	}
	if len(parts[1]) != 32 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}

func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x%x", time.Now().UnixNano(), time.Now().UnixMicro())
	}
	return hex.EncodeToString(b)
}

func generateShortID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func logSessionEvent(r *http.Request, userID string, isAuthenticated bool) {
	status := "anonymous"
	if isAuthenticated {
		status = "authenticated"
	}
	log.Printf("[VICI] User: %s (%s) | Path: %s | Method: %s", userID, status, r.URL.Path, r.Method)
}

func GetUserContext(r *http.Request) (string, bool, map[string]string) {
	userID, _ := r.Context().Value(UserIDKey).(string)
	isAuthenticated, _ := r.Context().Value(IsAuthenticatedKey).(bool)

	var metadata map[string]string
	if m, ok := r.Context().Value(UserMetadataKey).(map[string]string); ok {
		metadata = m
	} else {
		metadata = make(map[string]string)
	}

	return userID, isAuthenticated, metadata
}
