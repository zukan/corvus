package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/repository"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, cfg config.Config) (http.Handler, *repository.Memory) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("test-application-key-32-bytes-long"))
	orgKey := base64.StdEncoding.EncodeToString([]byte("test-organization-key-32bytes-long"))
	a, err := auth.New(key, orgKey, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1048576
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 1000
	}
	r := repository.NewMemory()
	return New(r, a, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil))), r
}
func call(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func data(t *testing.T, w *httptest.ResponseRecorder, code int) map[string]any {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status=%d body=%s want %d", w.Code, w.Body.String(), code)
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	v, _ := obj["data"].(map[string]any)
	return v
}
func bootstrap(t *testing.T, h http.Handler) (string, string, string) {
	t.Helper()
	d := data(t, call(h, "POST", "/v1/organizations", `{"organizationName":"Example Organization","organizationEmail":"org@example.com","secret":"example-secret","adminName":"Example Admin","adminEmail":"admin@example.com","adminPassword":"example-password"}`, ""), 201)
	org := d["organizationToken"].(string)
	app := data(t, call(h, "POST", "/v1/applications", `{"name":"Example application"}`, org), 201)
	return org, app["applicationToken"].(string), d["organizationId"].(string)
}
func TestTenantLifecycle(t *testing.T) {
	h, r := testServer(t, config.Config{})
	org, token, orgID := bootstrap(t, h)
	login := data(t, call(h, "POST", "/v1/organizations/token", `{"email":"admin@example.com","password":"example-password","organizationId":"`+orgID+`"}`, ""), 200)
	if login["organizationToken"] == "" {
		t.Fatal("missing token")
	}
	d := data(t, call(h, "POST", "/v1/comments", `{"content":"Hello","ownerId":"user-1","refId":"post-1"}`, token), 201)
	id := d["commentId"].(string)
	data(t, call(h, "POST", "/v1/applications/"+d["applicationId"].(string)+"/token", "", org), 201)
	data(t, call(h, "GET", "/v1/comments/"+id, "", token), 200)
	data(t, call(h, "PATCH", "/v1/comments/"+id, `{"content":"Edited","ownerId":"user-1"}`, token), 200)
	data(t, call(h, "DELETE", "/v1/comments/"+id, `{"ownerId":"user-1"}`, token), 200)
	if w := call(h, "GET", "/v1/comments/"+id, "", token); w.Code != 404 {
		t.Fatal(w.Code)
	}
	saved, err := r.GetOrganization(context.Background(), orgID)
	if err != nil || saved.Secret == "example-secret" {
		t.Fatal("secret stored unprotected")
	}
}
func TestOperationalMiddleware(t *testing.T) {
	t.Run("health", func(t *testing.T) {
		h, _ := testServer(t, config.Config{})
		for _, p := range []string{"/healthz", "/readyz"} {
			if w := call(h, "GET", p, "", ""); w.Code != 200 {
				t.Fatal(p, w.Code)
			}
		}
	})
	t.Run("body_limit", func(t *testing.T) {
		h, _ := testServer(t, config.Config{MaxBodyBytes: 10})
		w := call(h, "POST", "/v1/organizations", strings.Repeat(" ", 11), "")
		if w.Code != 413 {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	t.Run("no_decompression", func(t *testing.T) {
		h, _ := testServer(t, config.Config{})
		r := httptest.NewRequest("POST", "/v1/organizations", bytes.NewReader([]byte{31, 139}))
		r.Header.Set("Content-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 415 {
			t.Fatal(w.Code)
		}
	})
	t.Run("rate_limit", func(t *testing.T) {
		h, _ := testServer(t, config.Config{RateLimit: 1})
		_ = call(h, "GET", "/v1/comments", "", "")
		if w := call(h, "GET", "/v1/comments", "", ""); w.Code != 429 {
			t.Fatal(w.Code)
		}
		if w := call(h, "GET", "/healthz", "", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	})
	t.Run("cors", func(t *testing.T) {
		h, _ := testServer(t, config.Config{CORSOrigins: []string{"https://example.com"}})
		r := httptest.NewRequest("OPTIONS", "/v1/comments", nil)
		r.Header.Set("Origin", "https://example.com")
		r.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
			t.Fatal(w.Code, w.Header())
		}
	})
}

func TestOversizedTrailingJSON(t *testing.T) {
	h, _ := testServer(t, config.Config{MaxBodyBytes: 16})
	w := call(h, "POST", "/v1/organizations", `{}`+strings.Repeat(" ", 30), "")
	if w.Code != 413 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInvalidAndUnauthorizedRequests(t *testing.T) {
	h, _ := testServer(t, config.Config{})
	org, token, orgID := bootstrap(t, h)
	_ = org
	for _, tc := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"multiple_json", "POST", "/v1/comments", `{"ownerId":"user","content":"hi"}{}`, token, 400},
		{"null_body", "POST", "/v1/comments", `null`, token, 400},
		{"wrong_type", "POST", "/v1/comments", `{"ownerId":123,"content":"hi"}`, token, 422},
		{"empty_content", "POST", "/v1/comments", `{"ownerId":"user","content":""}`, token, 422},
		{"array_body", "POST", "/v1/comments", `[]`, token, 400},
		{"bad_page", "GET", "/v1/comments?page=0", "", token, 422},
		{"bad_sort", "GET", "/v1/comments?sort=other", "", token, 422},
		{"bad_flag", "GET", "/v1/comments?isFlagged=other", "", token, 422},
		{"bad_id", "GET", "/v1/comments/nope", "", token, 422},
		{"invalid_token", "GET", "/v1/comments", "", "wrong-token", 401},
		{"wrong_password", "POST", "/v1/organizations/token", `{"email":"admin@example.com","password":"wrong-password","organizationId":"` + orgID + `"}`, "", 401},
		{"unknown_email", "POST", "/v1/organizations/token", `{"email":"missing@example.com","password":"wrong-password","organizationId":"` + orgID + `"}`, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(h, tc.method, tc.path, tc.body, tc.token)
			if w.Code != tc.status {
				t.Fatalf("status=%d want%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

type unavailableRepo struct{ repository.Repository }

func (unavailableRepo) Ping(context.Context) error  { return io.ErrUnexpectedEOF }
func (unavailableRepo) Ready(context.Context) error { return io.ErrUnexpectedEOF }
func TestReadinessFailureDoesNotExposeDatabaseError(t *testing.T) {
	h := New(unavailableRepo{}, nil, config.Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	w := call(h, "GET", "/readyz", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type schemaUnavailableRepo struct{ repository.Repository }

func (schemaUnavailableRepo) Ping(context.Context) error  { return nil }
func (schemaUnavailableRepo) Ready(context.Context) error { return io.ErrUnexpectedEOF }

func TestReadinessChecksSchemaAfterDatabasePing(t *testing.T) {
	h := New(schemaUnavailableRepo{Repository: repository.NewMemory()}, nil, config.Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	w := call(h, "GET", "/readyz", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestForwardedAddressCannotBypassRateLimit(t *testing.T) {
	h, _ := testServer(t, config.Config{RateLimit: 1})
	for i, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		r := httptest.NewRequest("GET", "/v1/comments", nil)
		r.Header.Set("X-Forwarded-For", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i == 1 && w.Code != 429 {
			t.Fatalf("status=%d", w.Code)
		}
	}
}
func TestOversizedAccountNameReturnsValidationError(t *testing.T) {
	h, _ := testServer(t, config.Config{})
	org, _, _ := bootstrap(t, h)
	b, _ := json.Marshal(map[string]string{"name": strings.Repeat("x", 256)})
	w := call(h, "POST", "/v1/applications", string(b), org)
	if w.Code != 422 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
