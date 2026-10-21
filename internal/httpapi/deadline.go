package httpapi

import (
	"context"
	"net/http"
	"time"
)

// A socket write deadline does not cancel database work. Bound the context
// passed to every handler (including authentication lookups and transactions).
func (a *api) withDeadline(next http.Handler) http.Handler {
	timeout := a.cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if a.cfg.WriteTimeout > 0 && a.cfg.WriteTimeout < timeout {
		timeout = a.cfg.WriteTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
