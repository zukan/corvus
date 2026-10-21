package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/repository"
)

type legacyPairs struct {
	Seed struct {
		ApplicationID    string `json:"applicationId"`
		CommentID        string `json:"commentId"`
		OtherCommentID   string `json:"otherCommentId"`
		ForeignCommentID string `json:"foreignCommentId"`
		CreatedAt        string `json:"createdAt"`
		OtherCreatedAt   string `json:"otherCreatedAt"`
		Comment          struct {
			RefID, OwnerID, Content, Origin    string
			Flags, UpVotes, DownVotes, Replies []string
		} `json:"comment"`
		OtherComment struct {
			RefID, OwnerID, Content, Origin    string
			Flags, UpVotes, DownVotes, Replies []string
		} `json:"otherComment"`
	} `json:"seed"`
	Pairs map[string]struct {
		Request struct {
			Method string            `json:"method"`
			Path   string            `json:"path"`
			Query  map[string]string `json:"query"`
			Body   json.RawMessage   `json:"body"`
		} `json:"request"`
		Response struct {
			Status int            `json:"status"`
			Body   map[string]any `json:"body"`
		} `json:"response"`
	} `json:"pairs"`
}

func readLegacyPairs(t *testing.T) legacyPairs {
	t.Helper()
	b, err := os.ReadFile("testdata/legacy_pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	var pairs legacyPairs
	if err := json.Unmarshal(b, &pairs); err != nil {
		t.Fatal(err)
	}
	return pairs
}

func newCompatibilityServer(t *testing.T, pairs legacyPairs, extra ...repository.Comment) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewMemory()
	orgID, adminID := "64a000000000000000000010", "64a000000000000000000011"
	if err := repo.CreateOrganization(ctx, repository.Organization{ID: orgID, Name: "Org", Email: "org@example.com"}, repository.Admin{
		ID: adminID, OrganizationID: orgID, Fullname: "Admin", Email: "admin@example.com", Password: "unused",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateApplication(ctx, repository.Application{ID: pairs.Seed.ApplicationID, OrganizationID: orgID, CreatedBy: adminID, Name: "App"}); err != nil {
		t.Fatal(err)
	}
	foreignOrgID, foreignAdminID := "64a000000000000000000020", "64a000000000000000000021"
	if err := repo.CreateOrganization(ctx, repository.Organization{ID: foreignOrgID, Name: "Other org", Email: "other@example.com"}, repository.Admin{
		ID: foreignAdminID, OrganizationID: foreignOrgID, Fullname: "Other admin", Email: "other-admin@example.com", Password: "unused",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateApplication(ctx, repository.Application{ID: "64a000000000000000000002", OrganizationID: foreignOrgID, CreatedBy: foreignAdminID, Name: "Other app"}); err != nil {
		t.Fatal(err)
	}
	secret := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	tokens, err := auth.New(secret, secret, 720*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokens.SignApplication(pairs.Seed.ApplicationID, adminID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, pairs.Seed.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	otherCreatedAt, err := time.Parse(time.RFC3339Nano, pairs.Seed.OtherCreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range append([]repository.Comment{
		{ID: pairs.Seed.CommentID, ApplicationID: pairs.Seed.ApplicationID, RefID: strptr(pairs.Seed.Comment.RefID), OwnerID: pairs.Seed.Comment.OwnerID, Content: pairs.Seed.Comment.Content, Origin: strptr(pairs.Seed.Comment.Origin), Flags: pairs.Seed.Comment.Flags, UpVotes: pairs.Seed.Comment.UpVotes, DownVotes: pairs.Seed.Comment.DownVotes, Replies: pairs.Seed.Comment.Replies, CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: pairs.Seed.OtherCommentID, ApplicationID: pairs.Seed.ApplicationID, RefID: strptr(pairs.Seed.OtherComment.RefID), OwnerID: pairs.Seed.OtherComment.OwnerID, Content: pairs.Seed.OtherComment.Content, Origin: strptr(pairs.Seed.OtherComment.Origin), Flags: pairs.Seed.OtherComment.Flags, UpVotes: pairs.Seed.OtherComment.UpVotes, DownVotes: pairs.Seed.OtherComment.DownVotes, Replies: pairs.Seed.OtherComment.Replies, CreatedAt: otherCreatedAt, UpdatedAt: otherCreatedAt},
		{ID: pairs.Seed.ForeignCommentID, ApplicationID: "64a000000000000000000002", RefID: strptr("private-ref"), OwnerID: "other@example.com", Content: "Private tenant comment", Origin: strptr("web"), CreatedAt: createdAt, UpdatedAt: createdAt},
	}, extra...) {
		if err := repo.CreateComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return New(repo, tokens, config.Config{MaxBodyBytes: 1 << 20, RateLimit: 10000}, slog.New(slog.NewTextHandler(io.Discard, nil))), token
}

func strptr(s string) *string { return &s }

func TestLegacyCommentRequestResponsePairs(t *testing.T) {
	pairs := readLegacyPairs(t)
	for _, name := range []string{"create", "getItem", "listPage", "listFlagged", "update", "delete", "missingContent", "missingBearer", "wrongOwnerPatch", "wrongOwnerDelete", "pageOverflow", "limitOver50", "crossTenantItem"} {
		t.Run(name, func(t *testing.T) {
			server, token := newCompatibilityServer(t, pairs)
			pair := pairs.Pairs[name]
			path := strings.NewReplacer(
				"$commentId", pairs.Seed.CommentID,
				"$applicationId", pairs.Seed.ApplicationID,
				"$foreignCommentId", pairs.Seed.ForeignCommentID,
			).Replace(pair.Request.Path)
			query := url.Values{}
			for key, value := range pair.Request.Query {
				query.Set(key, value)
			}
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
			var body io.Reader
			if len(pair.Request.Body) != 0 && string(pair.Request.Body) != "null" {
				body = bytes.NewReader(pair.Request.Body)
			}
			req := httptest.NewRequest(pair.Request.Method, path, body)
			if name != "missingBearer" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			res := httptest.NewRecorder()
			server.ServeHTTP(res, req)
			if res.Code != pair.Response.Status {
				t.Fatalf("status = %d, want %d; body=%s", res.Code, pair.Response.Status, res.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, res.Body.String())
			}
			want := substitute(pair.Response.Body, map[string]string{
				"$applicationId":    pairs.Seed.ApplicationID,
				"$commentId":        pairs.Seed.CommentID,
				"$otherCommentId":   pairs.Seed.OtherCommentID,
				"$foreignCommentId": pairs.Seed.ForeignCommentID,
			})
			if err := matchLegacyValue(want, got, "response"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func substitute(value any, replacements map[string]string) any {
	switch v := value.(type) {
	case string:
		if replacement, ok := replacements[v]; ok {
			return replacement
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = substitute(item, replacements)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = substitute(item, replacements)
		}
		return out
	default:
		return value
	}
}

func matchLegacyValue(want, got any, path string) error {
	if marker, ok := want.(string); ok {
		switch marker {
		case "$any-string":
			if _, ok := got.(string); !ok {
				return testMismatch(path, want, got)
			}
			return nil
		case "$any-time":
			value, ok := got.(string)
			if !ok {
				return testMismatch(path, want, got)
			}
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return testMismatch(path, want, got)
			}
			return nil
		}
	}
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok || len(expected) != len(actual) {
			return testMismatch(path, want, got)
		}
		for key, child := range expected {
			value, exists := actual[key]
			if !exists {
				return testMismatch(path+"."+key, child, "<missing>")
			}
			if err := matchLegacyValue(child, value, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	case []any:
		actual, ok := got.([]any)
		if !ok || len(expected) != len(actual) {
			return testMismatch(path, want, got)
		}
		for i := range expected {
			if err := matchLegacyValue(expected[i], actual[i], path+"[index]"); err != nil {
				return err
			}
		}
		return nil
	default:
		if !reflect.DeepEqual(want, got) {
			return testMismatch(path, want, got)
		}
		return nil
	}
}

func testMismatch(path string, want, got any) error {
	return &compatibilityMismatch{path: path, want: want, got: got}
}

type compatibilityMismatch struct {
	path string
	want any
	got  any
}

func (e *compatibilityMismatch) Error() string {
	return e.path + " mismatch: got " + toJSON(e.got) + ", want " + toJSON(e.want)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
