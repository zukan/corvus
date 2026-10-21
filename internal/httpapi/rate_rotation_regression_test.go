package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zukan/corvus/internal/config"
)

func TestIPv6RotationCannotExhaustAdmission(t *testing.T) {
	h, _ := testServer(t, config.Config{RateLimit: 1})
	admitted := 0
	for i := 0; i < 10000; i++ {
		req := httptest.NewRequest("GET", "/v1/comments", nil)
		req.RemoteAddr = fmt.Sprintf("[2001:db8:1234:5678::%x]:4321", i)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusTooManyRequests {
			admitted++
		}
	}
	if admitted != 1 {
		t.Errorf("one IPv6 /64 admitted %d requests, want 1", admitted)
	}
	req := httptest.NewRequest("GET", "/v1/comments", nil)
	req.RemoteAddr = "192.0.2.50:4321"
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code == http.StatusTooManyRequests {
		t.Error("IPv6 rotation denied an unrelated IPv4 client")
	}
}
