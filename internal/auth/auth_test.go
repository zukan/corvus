package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

func TestNewRequiresNonemptyBase64Secrets(t *testing.T) {
	for _, tc := range []struct{ name, app, org string }{
		{"empty application", "", base64.StdEncoding.EncodeToString([]byte("org"))},
		{"empty decoded application", base64.StdEncoding.EncodeToString(nil), base64.StdEncoding.EncodeToString([]byte("org"))},
		{"invalid application", "%%%", base64.StdEncoding.EncodeToString([]byte("org"))},
		{"empty organization", base64.StdEncoding.EncodeToString([]byte("app")), ""},
		{"empty decoded organization", base64.StdEncoding.EncodeToString([]byte("app")), base64.StdEncoding.EncodeToString(nil)},
		{"invalid organization", base64.StdEncoding.EncodeToString([]byte("app")), "%%%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.app, tc.org, time.Hour, true); err == nil {
				t.Fatal("expected invalid secret error")
			}
		})
	}
}

func TestNewRequiresPositiveTTL(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("secret"))
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := New(secret, secret, ttl, true); err == nil {
			t.Fatalf("New accepted ttl %s", ttl)
		}
	}
}

func TestParseBearerIsCaseInsensitiveAndReturnsTenantClaims(t *testing.T) {
	s := newService(t, true)
	token, err := s.SignApplication("app-1", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.Parse("bEaReR "+token, "application")
	if err != nil {
		t.Fatal(err)
	}
	if claims.ApplicationID != "app-1" || claims.AdminID != "admin-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestParseRejectsWrongAlgorithmAndWrongTenantKey(t *testing.T) {
	s := newService(t, true)
	token, err := s.SignApplication("app-1", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Parse("Bearer "+token, "organization"); err == nil {
		t.Fatal("accepted token signed with another tenant key")
	}
	if _, err := s.Parse("Bearer "+makeToken(t, "none", nil, nil), "application"); err == nil {
		t.Fatal("accepted none algorithm")
	}
}

func TestParseValidatesExpirationAndNotBeforeWhenPresent(t *testing.T) {
	s := newService(t, true)
	key := []byte("application-secret")
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims
	}{
		{"expired", jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}},
		{"not active yet", jwt.MapClaims{"nbf": time.Now().Add(time.Minute).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, tc.claims)
			signed, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Parse("Bearer "+signed, "application"); err == nil {
				t.Fatal("accepted token outside its validity window")
			}
		})
	}
}

func TestParseMapsJWTErrorsToLegacyMessages(t *testing.T) {
	s := newService(t, true)
	key := []byte("application-secret")
	makeSigned := func(method jwt.SigningMethod, claims jwt.Claims, signingKey []byte) string {
		t.Helper()
		token := jwt.NewWithClaims(method, claims)
		signed, err := token.SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	validClaims := func() *Claims {
		return &Claims{RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	}
	tests := []struct {
		name, token, want string
	}{
		{"expired", makeSigned(jwt.SigningMethodHS256, &Claims{RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}, key), "jwt expired"},
		{"not active", makeSigned(jwt.SigningMethodHS256, &Claims{RegisteredClaims: jwt.RegisteredClaims{NotBefore: jwt.NewNumericDate(time.Now().Add(time.Minute))}}, key), "jwt not active"},
		{"invalid signature", makeSigned(jwt.SigningMethodHS256, validClaims(), []byte("wrong-secret")), "invalid signature"},
		{"invalid algorithm", makeSigned(jwt.SigningMethodHS384, validClaims(), key), "invalid algorithm"},
		{"malformed", "nope", "jwt malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Parse("Bearer "+tc.token, "application")
			var authErr *Error
			if !errors.As(err, &authErr) || authErr.Status != 401 || authErr.Message != tc.want {
				t.Fatalf("got %#v, want 401 %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsMalformedAuthorizationHeaderAsBadRequest(t *testing.T) {
	s := newService(t, true)
	_, err := s.Parse("Bearer one two", "application")
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Status != 400 || authErr.Message != "Format is Authorization: Bearer [token]" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestParseRequiresAuthorizationOnlyWhenConfigured(t *testing.T) {
	required := newService(t, true)
	_, err := required.Parse("", "application")
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Status != 401 || authErr.Message != "No authorization token was found" {
		t.Fatalf("unexpected error: %#v", err)
	}
	optional := newService(t, false)
	claims, err := optional.Parse("", "application")
	if err != nil || claims.ApplicationID != "" {
		t.Fatalf("expected empty claims, got %+v, %v", claims, err)
	}
}

func TestSignAndParseOrganizationClaims(t *testing.T) {
	s := newService(t, true)
	token, err := s.SignOrganization("org-1", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.Parse("Bearer "+token, "organization")
	if err != nil {
		t.Fatal(err)
	}
	if claims.OrganizationID != "org-1" || claims.AdminID != "admin-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestPasswordHelpersRejectOverlongPasswordsAndVerifyBcrypt(t *testing.T) {
	password := strings.Repeat("x", 73)
	if _, err := HashPassword(password); err == nil {
		t.Fatal("expected overlong password error")
	}
	if err := ComparePassword("$2a$invalid", strings.Repeat("x", 73)); err == nil {
		t.Fatal("expected overlong compare error")
	}
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := ComparePassword(hash, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := ComparePassword(hash, "wrong"); err == nil {
		t.Fatal("accepted wrong password")
	}
}

func makeToken(t *testing.T, alg string, _, _ any) string {
	t.Helper()
	token := jwt.New(jwt.GetSigningMethod(alg))
	token.Claims = jwt.MapClaims{}
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func newService(t *testing.T, required bool) *Service {
	t.Helper()
	s, err := New(base64.StdEncoding.EncodeToString([]byte("application-secret")), base64.StdEncoding.EncodeToString([]byte("organization-secret")), time.Hour, required)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
