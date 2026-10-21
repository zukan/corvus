package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/config"
)

func TestRateLimitClientKeyNormalizesIPv6Subnet(t *testing.T) {
	first := netip.MustParseAddr("2001:db8:1234:5678::1")
	rotated := netip.MustParseAddr("2001:db8:1234:5678:ffff::9")
	otherSubnet := netip.MustParseAddr("2001:db8:1234:5679::1")

	if got, want := rateLimitClientKey(first, 64), "2001:db8:1234:5678::/64"; got != want {
		t.Fatalf("first key=%q want %q", got, want)
	}
	if got := rateLimitClientKey(rotated, 64); got != rateLimitClientKey(first, 64) {
		t.Fatalf("rotated IPv6 key=%q want same as %q", got, rateLimitClientKey(first, 64))
	}
	if got := rateLimitClientKey(otherSubnet, 64); got == rateLimitClientKey(first, 64) {
		t.Fatalf("different /64 unexpectedly shared key %q", got)
	}
	if got, want := rateLimitClientKey(netip.MustParseAddr("192.0.2.1"), 64), "192.0.2.1"; got != want {
		t.Fatalf("IPv4 key=%q want %q", got, want)
	}
}

func TestMiddlewareRateLimitsIPv6ByConfiguredSubnet(t *testing.T) {
	h, _ := testServer(t, config.Config{RateLimit: 1, IPv6RateLimitPrefix: 64})
	request := func(remoteAddr string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/comments", nil)
		r.RemoteAddr = remoteAddr
		r.Header.Set("Authorization", "Bearer invalid")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("[2001:db8:1234:5678::1]:1000"); got != http.StatusUnauthorized {
		t.Fatalf("first status=%d want %d", got, http.StatusUnauthorized)
	}
	if got := request("[2001:db8:1234:5678:ffff::9]:1001"); got != http.StatusTooManyRequests {
		t.Fatalf("rotated address status=%d want %d", got, http.StatusTooManyRequests)
	}
	if got := request("[2001:db8:1234:5679::1]:1002"); got != http.StatusUnauthorized {
		t.Fatalf("different subnet status=%d want %d", got, http.StatusUnauthorized)
	}
}

func TestLimiterEvictsOldestEntryInsteadOfRejectingUnseenClient(t *testing.T) {
	lim := &limiter{clients: make(map[string]bucket), limit: 1, maxClients: 2}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if !lim.allow("one", now) || !lim.allow("two", now.Add(time.Second)) {
		t.Fatal("initial clients were rejected")
	}
	if lim.allow("one", now.Add(2*time.Second)) {
		t.Fatal("client exceeded its request limit")
	}
	if !lim.allow("three", now.Add(3*time.Second)) {
		t.Fatal("new client was rejected at capacity")
	}
	if _, ok := lim.clients["one"]; !ok {
		t.Fatal("recently observed client entry was evicted")
	}
	if _, ok := lim.clients["two"]; ok {
		t.Fatal("least recently observed client entry was not evicted")
	}
}
