package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type bucket struct {
	start time.Time
	count int
}
type limiter struct {
	mu      sync.Mutex
	clients map[string]bucket
	limit   int
}

func (l *limiter) allow(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, exists := l.clients[client]
	if !exists || now.Sub(b.start) >= time.Minute {
		b = bucket{start: now}
	}
	if !exists && len(l.clients) >= 10000 {
		for k, v := range l.clients {
			if now.Sub(v.start) >= time.Minute {
				delete(l.clients, k)
			}
		}
		if len(l.clients) >= 10000 {
			return false
		}
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	l.clients[client] = b
	return true
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (a *api) middleware(next http.Handler) http.Handler {
	limits := &limiter{clients: map[string]bucket{}, limit: a.cfg.RateLimit}
	return http.HandlerFunc(func(original http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w := &responseWriter{ResponseWriter: original}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recover() != nil {
				if w.status == 0 {
					failure(w, 500, "Internal server error", nil)
				}
				a.log.Error("request panic")
			}
			code := w.status
			if code == 0 {
				code = 200
			}
			a.log.Info("request", "method", r.Method, "route", r.Pattern, "status", code, "duration_ms", time.Since(started).Milliseconds())
		}()
		origin := r.Header.Get("Origin")
		if len(a.cfg.CORSOrigins) > 0 {
			w.Header().Add("Vary", "Origin")
		}
		allowed := ""
		if origin != "" {
			for _, v := range a.cfg.CORSOrigins {
				if v == "*" || v == origin {
					allowed = v
					break
				}
			}
		}
		if allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
		}
		if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
			if allowed == "" {
				failure(w, 403, "Origin not allowed", nil)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if !limits.allow(ip, started) {
				w.Header().Set("Retry-After", "60")
				failure(w, 429, "Too many requests", nil)
				return
			}
		}
		if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			failure(w, 415, "Content-Encoding is not supported", nil)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
		// Express accepts a trailing slash on these routes; normalize without redirecting a body.
		if len(r.URL.Path) > 1 && strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		}
		next.ServeHTTP(w, r)
	})
}
