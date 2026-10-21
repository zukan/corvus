package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/repository"
)

type stalledRepository struct {
	repository.Repository
	seen context.Context
}

func (s *stalledRepository) GetApplication(context.Context, string) (repository.Application, error) {
	return repository.Application{OrganizationID: "64a000000000000000000010"}, nil
}
func (s *stalledRepository) GetOrganization(context.Context, string) (repository.Organization, error) {
	return repository.Organization{}, nil
}

func (s *stalledRepository) ListComments(ctx context.Context, _ repository.Filter) ([]repository.Comment, int, error) {
	s.seen = ctx
	if _, ok := ctx.Deadline(); !ok {
		return nil, 0, errors.New("missing database deadline")
	}
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func TestRequestDeadlineCancelsRepositoryWork(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("deadline-test-key-with-32-bytes!!"))
	t.Setenv("MYSQL_DSN", "unused")
	t.Setenv("APP_SECRET", key)
	t.Setenv("ORG_SECRET", key)
	t.Setenv("REQUEST_TIMEOUT", "20ms")
	t.Setenv("WRITE_TIMEOUT", "1s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.New(key, key, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokens.SignApplication("64a000000000000000000001", "64a000000000000000000011")
	if err != nil {
		t.Fatal(err)
	}
	for _, earlier := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured", true: "earlier_parent"}[earlier], func(t *testing.T) {
			repo := &stalledRepository{}
			h := New(repo, tokens, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			req := httptest.NewRequest("GET", "/v1/comments", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			if earlier {
				ctx, cancel := context.WithTimeout(req.Context(), 5*time.Millisecond)
				defer cancel()
				req = req.WithContext(ctx)
			}
			start := time.Now()
			h.ServeHTTP(httptest.NewRecorder(), req)
			if repo.seen == nil {
				t.Fatal("repository was not reached")
			}
			deadline, ok := repo.seen.Deadline()
			if !ok {
				t.Fatal("repository context has no deadline")
			}
			if deadline.After(start.Add(100 * time.Millisecond)) {
				t.Fatal("request deadline was not applied")
			}
			if earlier {
				parent, _ := req.Context().Deadline()
				if !deadline.Equal(parent) {
					t.Fatal("parent deadline was extended")
				}
			}
			if !errors.Is(repo.seen.Err(), context.DeadlineExceeded) {
				t.Fatalf("repository context error = %v", repo.seen.Err())
			}
		})
	}
}
