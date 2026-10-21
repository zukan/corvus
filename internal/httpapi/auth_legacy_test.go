package httpapi

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

func TestOrganizationTokenAcceptsLegacyBcryptTruncatedPassword(t *testing.T) {
	const orgID, adminID = "64a000000000000000000010", "64a000000000000000000011"
	password := strings.Repeat("x", 72)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMemory()
	if err := repo.CreateOrganization(t.Context(), repository.Organization{ID: orgID, Name: "Org", Email: "org@example.com"}, repository.Admin{
		ID: adminID, OrganizationID: orgID, Fullname: "Admin", Email: "admin@example.com", Password: string(hash),
	}); err != nil {
		t.Fatal(err)
	}
	secret := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	tokens, err := auth.New(secret, secret, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	h := New(repo, tokens, config.Config{MaxBodyBytes: 1 << 20, RateLimit: 100}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"email":"admin@example.com","password":"` + password + `legacy-import-suffix","organizationId":"` + orgID + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/token", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), http.StatusOK)
	}
}
