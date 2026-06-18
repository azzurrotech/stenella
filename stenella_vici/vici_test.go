package stenella_vici

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.CookieName != "vici_session" {
		t.Errorf("CookieName = %q, want %q", cfg.CookieName, "vici_session")
	}
	if cfg.CookieMaxAge != 86400 {
		t.Errorf("CookieMaxAge = %d, want 86400", cfg.CookieMaxAge)
	}
	if cfg.AuthHeaderKey != "Authorization" {
		t.Errorf("AuthHeaderKey = %q, want %q", cfg.AuthHeaderKey, "Authorization")
	}
	if cfg.AuthParamKey != "auth_token" {
		t.Errorf("AuthParamKey = %q, want %q", cfg.AuthParamKey, "auth_token")
	}
	if !cfg.EnableLogging {
		t.Error("EnableLogging should be true by default")
	}
}

func TestAnonymousSession(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SecretKey = []byte("test-secret")

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, auth, _ := GetUserContext(r)
		if auth {
			t.Error("Expected anonymous user")
		}
		if id == "" {
			t.Error("Expected non-empty user ID")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Error("Expected session cookie to be set")
	} else {
		if cookies[0].Name != cfg.CookieName {
			t.Errorf("Expected cookie name %s, got %s", cfg.CookieName, cookies[0].Name)
		}
		if cookies[0].Value == "" {
			t.Error("Cookie value should not be empty")
		}
	}
}

func TestAuthenticatedByHeader(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SecretKey = []byte("test-secret")

	validToken := "valid_" + generateShortID()

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, auth, meta := GetUserContext(r)
		if !auth {
			t.Error("Expected authenticated user")
		}
		if meta["auth_method"] != "header" {
			t.Errorf("Expected auth_method 'header', got %q", meta["auth_method"])
		}
		if !strings.HasPrefix(id, "auth_") {
			t.Errorf("Expected user ID to start with 'auth_', got %q", id)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", validToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want 200", w.Code)
	}
}

func TestAuthenticatedByURLParam(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SecretKey = []byte("test-secret")

	validToken := "valid_" + generateShortID()

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, auth, meta := GetUserContext(r)
		if !auth {
			t.Error("Expected authenticated user")
		}
		if meta["auth_method"] != "url_param" {
			t.Errorf("Expected auth_method 'url_param', got %q", meta["auth_method"])
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test?auth_token="+validToken, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want 200", w.Code)
	}
}

func TestInvalidTokenRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SecretKey = []byte("test-secret")

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, auth, _ := GetUserContext(r)
		if auth {
			t.Error("Expected no authentication for invalid token")
		}
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no prefix", "abcdef1234567890abcdef1234567890"},
		{"wrong prefix", "invalid_abcdef1234567890abcdef1234567890"},
		{"short hex", "valid_abc"},
		{"not hex", "valid_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", tt.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		})
	}
}

func TestContextPropagation(t *testing.T) {
	cfg := DefaultConfig()

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, auth, meta := GetUserContext(r)
		if id == "" {
			t.Error("User ID not propagated")
		}
		if auth {
			t.Error("Should be anonymous without credentials")
		}
		if meta == nil {
			t.Error("Metadata should not be nil")
		}
		w.Write([]byte(id))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want 200", w.Code)
	}
	if w.Body.String() == "" {
		t.Error("Response body should contain user ID")
	}
}

func TestZeroValueConfig(t *testing.T) {
	cfg := Config{}

	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _, _ := GetUserContext(r)
		if id == "" {
			t.Error("Expected non-empty user ID with zero config")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if cookies := w.Result().Cookies(); len(cookies) > 0 {
		if cookies[0].Name != "vici_session" {
			t.Errorf("Expected default cookie name 'vici_session', got %q", cookies[0].Name)
		}
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1 := generateSessionID()
	id2 := generateSessionID()

	if id1 == "" {
		t.Error("Session ID should not be empty")
	}
	if len(id1) != 64 {
		t.Errorf("Session ID length = %d, want 64", len(id1))
	}
	if id1 == id2 {
		t.Error("Two session IDs should be different")
	}
}

func TestGenerateShortID(t *testing.T) {
	id1 := generateShortID()
	id2 := generateShortID()

	if id1 == "" {
		t.Error("Short ID should not be empty")
	}
	if len(id1) != 32 {
		t.Errorf("Short ID length = %d, want 32", len(id1))
	}
	if id1 == id2 {
		t.Error("Two short IDs should be different")
	}
}

func TestIsValidToken(t *testing.T) {
	secret := []byte("my-secret-key")

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"empty", "", false},
		{"nil secret", "anything", false},
		{"valid format", "valid_aabbccdd11223344aabbccdd11223344", true},
		{"wrong prefix", "invalid_aabbccdd11223344aabbccdd11223344", false},
		{"short value", "valid_abc", false},
		{"not hex", "valid_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidToken(tt.token, secret)
			if got != tt.want {
				t.Errorf("isValidToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}

	t.Run("nil secret always false", func(t *testing.T) {
		if isValidToken("valid_aabbccdd11223344aabbccdd11223344", nil) {
			t.Error("isValidToken with nil secret should be false")
		}
	})
}

func TestExtractUserContextNoAuth(t *testing.T) {
	cfg := DefaultConfig()
	req := httptest.NewRequest("GET", "/test", nil)

	userID, isAuth, meta := extractUserContext(req, cfg)
	if isAuth {
		t.Error("Expected not authenticated")
	}
	if userID == "" {
		t.Error("Expected non-empty user ID")
	}
	if meta == nil {
		t.Error("Expected non-nil metadata")
	}
}

func TestMiddlewarePreservesNextHandler(t *testing.T) {
	cfg := DefaultConfig()

	var nextCalled bool
	handler := Middleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !nextCalled {
		t.Error("Next handler was not called")
	}
	if w.Code != http.StatusTeapot {
		t.Errorf("Status = %d, want 418", w.Code)
	}
}

func TestGetUserContextWithoutMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, auth, meta := GetUserContext(r)
		if id != "" {
			t.Errorf("Expected empty user ID without middleware, got %q", id)
		}
		if auth {
			t.Error("Expected not authenticated without middleware")
		}
		if meta == nil {
			t.Error("Expected non-nil metadata even without middleware")
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
}
