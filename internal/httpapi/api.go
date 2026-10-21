// Package httpapi implements the supported legacy HTTP contract.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/repository"
)

type api struct {
	repo   repository.Repository
	tokens *auth.Service
	cfg    config.Config
	log    *slog.Logger
}
type object = map[string]any

func New(repo repository.Repository, tokens *auth.Service, cfg config.Config, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1048576
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 120
	}
	a := &api{repo: repo, tokens: tokens, cfg: cfg, log: logger}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, object{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := repo.Ready(ctx); err != nil {
			failure(w, 503, "Service unavailable", nil)
			return
		}
		write(w, 200, object{"status": "ok"})
	})
	m.HandleFunc("POST /v1/organizations", a.createOrganization)
	m.HandleFunc("POST /v1/organizations/token", a.organizationToken)
	m.HandleFunc("POST /v1/applications", a.createApplication)
	m.HandleFunc("POST /v1/applications/{applicationId}/token", a.applicationToken)
	m.HandleFunc("POST /v1/comments", a.createComment)
	m.HandleFunc("GET /v1/comments", a.listComments)
	m.HandleFunc("GET /v1/comments/{commentId}", a.getComment)
	m.HandleFunc("PATCH /v1/comments/{commentId}", a.updateComment)
	m.HandleFunc("DELETE /v1/comments/{commentId}", a.deleteComment)
	// A JSON fallback also covers methods outside the supported compatibility surface.
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { failure(w, 404, "Not found", nil) })
	return a.withDeadline(a.middleware(m))
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func success(w http.ResponseWriter, status int, d any, message string) {
	write(w, status, object{"data": d, "status": "success", "message": message})
}
func failure(w http.ResponseWriter, status int, message string, d any) {
	if d == nil {
		d = []any{}
	}
	write(w, status, object{"status": "error", "error": message, "data": d})
}
func (a *api) internal(w http.ResponseWriter) { failure(w, 500, "Internal server error", nil) }
func identifier() string                      { var b [12]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func validID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func (a *api) body(w http.ResponseWriter, r *http.Request) (object, bool) {
	var b object
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&b)
	if err == nil {
		var extra any
		if trailingErr := dec.Decode(&extra); trailingErr != io.EOF {
			err = trailingErr
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			failure(w, 413, "request entity too large", nil)
		} else {
			failure(w, 400, "Invalid JSON", nil)
		}
		return nil, false
	}
	if b == nil {
		failure(w, 400, "Invalid JSON", nil)
		return nil, false
	}
	return b, true
}
func validateStrings(w http.ResponseWriter, b object, required, optional []string, where string) bool {
	errs := []string{}
	for _, key := range append(append([]string{}, required...), optional...) {
		v, exists := b[key]
		isRequired := false
		for _, k := range required {
			if k == key {
				isRequired = true
			}
		}
		if !exists {
			if isRequired {
				errs = append(errs, fmt.Sprintf("\"%s\" is required in %s", key, where))
			}
			continue
		}
		s, ok := v.(string)
		if !ok {
			errs = append(errs, fmt.Sprintf("\"%s\" must be a string in %s", key, where))
		} else if s == "" {
			errs = append(errs, fmt.Sprintf("\"%s\" is not allowed to be empty in %s", key, where))
		} else {
			maxChars := map[string]int{"name": 255, "organizationName": 255, "adminName": 255, "email": 320, "organizationEmail": 320, "adminEmail": 320}[key]
			if maxChars > 0 && utf8.RuneCountInString(s) > maxChars {
				errs = append(errs, fmt.Sprintf("\"%s\" length must be less than or equal to %d characters long in %s", key, maxChars, where))
			}
		}
	}
	if len(errs) > 0 {
		failure(w, 422, "Invalid input supplied", errs)
		return false
	}
	return true
}
func str(b object, key string) string { s, _ := b[key].(string); return s }
func optional(b object, key string) *string {
	if v, ok := b[key].(string); ok {
		return &v
	}
	return nil
}
func (a *api) claims(w http.ResponseWriter, r *http.Request, kind string) (auth.Claims, bool) {
	c, err := a.tokens.Parse(r.Header.Get("Authorization"), kind)
	if err != nil {
		var e *auth.Error
		if errors.As(err, &e) {
			failure(w, e.Status, e.Message, nil)
		} else {
			failure(w, 401, "Unauthorized", nil)
		}
		return c, false
	}
	if r.Header.Get("Authorization") == "" {
		failure(w, 422, "Invalid input supplied", []string{`"authorization" is required in headers`})
		return c, false
	}
	if kind == "application" {
		if !validID(c.ApplicationID) {
			failure(w, 401, "unauthorised request", nil)
			return c, false
		}
		app, e := a.repo.GetApplication(r.Context(), c.ApplicationID)
		if e != nil {
			a.authLookupError(w, e)
			return c, false
		}
		_, e = a.repo.GetOrganization(r.Context(), app.OrganizationID)
		if e != nil {
			a.authLookupError(w, e)
			return c, false
		}
	} else {
		if !validID(c.OrganizationID) || !validID(c.AdminID) {
			failure(w, 401, "Unauthorized", nil)
			return c, false
		}
		admin, e := a.repo.GetAdmin(r.Context(), c.AdminID)
		if e != nil {
			a.authLookupError(w, e)
			return c, false
		}
		if admin.OrganizationID != c.OrganizationID {
			failure(w, 401, "You are not authorized to access this resource", nil)
			return c, false
		}
		_, e = a.repo.GetOrganization(r.Context(), c.OrganizationID)
		if e != nil {
			a.authLookupError(w, e)
			return c, false
		}
	}
	return c, true
}
func (a *api) authLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		failure(w, 401, "You are not authorized to access this resource", nil)
	} else {
		a.internal(w)
	}
}
func commentData(c repository.Comment) object {
	d := object{"commentId": c.ID, "applicationId": c.ApplicationID, "ownerId": c.OwnerID, "content": c.Content, "numOfVotes": len(c.UpVotes) + len(c.DownVotes), "numOfUpVotes": len(c.UpVotes), "numOfDownVotes": len(c.DownVotes), "numOfFlags": len(c.Flags), "numOfReplies": len(c.Replies), "createdAt": iso(c.CreatedAt), "updatedAt": iso(c.UpdatedAt)}
	if c.RefID != nil {
		d["refId"] = *c.RefID
	}
	if c.Origin != nil {
		d["origin"] = *c.Origin
	}
	return d
}
func (a *api) createComment(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "application")
	if !ok {
		return
	}
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"ownerId", "content"}, []string{"refId", "origin"}, "body") {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	v := repository.Comment{ID: identifier(), ApplicationID: c.ApplicationID, OwnerID: str(b, "ownerId"), Content: str(b, "content"), RefID: optional(b, "refId"), Origin: optional(b, "origin"), CreatedAt: now, UpdatedAt: now}
	if err := a.repo.CreateComment(r.Context(), v); err != nil {
		a.internal(w)
		return
	}
	success(w, 201, commentData(v), "Comment created successfully")
}
func (a *api) getComment(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "application")
	if !ok {
		return
	}
	id := r.PathValue("commentId")
	if !validID(id) {
		failure(w, 422, "invalid ID", nil)
		return
	}
	v, err := a.repo.GetComment(r.Context(), c.ApplicationID, id)
	if errors.Is(err, repository.ErrNotFound) {
		failure(w, 404, fmt.Sprintf("Comment with the ID %s doesn't exist or has been removed", id), nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 200, commentData(v), "Comment Retrieved Successfully")
}
func (a *api) updateComment(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "application")
	if !ok {
		return
	}
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"content", "ownerId"}, nil, "body") {
		return
	}
	v, err := a.repo.UpdateComment(r.Context(), c.ApplicationID, r.PathValue("commentId"), str(b, "ownerId"), str(b, "content"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(w, 404, "Comment not found", nil)
		return
	}
	if errors.Is(err, repository.ErrForbidden) {
		failure(w, 403, "Sorry, comment cannot be updated or Unauthorized", nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 200, object{"content": str(b, "content"), "ownerId": str(b, "ownerId"), "updatedAt": iso(v.UpdatedAt)}, "Updated sucessfully")
}
func (a *api) deleteComment(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "application")
	if !ok {
		return
	}
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"ownerId"}, nil, "body") {
		return
	}
	id := r.PathValue("commentId")
	if !validID(id) {
		failure(w, 404, "Comment not found", nil)
		return
	}
	v, err := a.repo.DeleteComment(r.Context(), c.ApplicationID, id, str(b, "ownerId"))
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrForbidden) {
		failure(w, 404, "Comment not found or unauthorized", nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 200, commentData(v), "Comment successfully deleted")
}
func (a *api) listComments(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "application")
	if !ok {
		return
	}
	q := r.URL.Query()
	b := object{}
	for _, k := range []string{"refId", "origin", "ownerId"} {
		if q.Has(k) {
			b[k] = q.Get(k)
		}
	}
	if !validateStrings(w, b, nil, []string{"refId", "origin", "ownerId"}, "query") {
		return
	}
	f := repository.Filter{ApplicationID: c.ApplicationID, RefID: optional(b, "refId"), Origin: optional(b, "origin"), OwnerID: optional(b, "ownerId"), Page: 1, Limit: 20}
	for _, p := range []struct {
		k string
		v *int
	}{{"page", &f.Page}, {"limit", &f.Limit}} {
		if q.Has(p.k) {
			v, e := strconv.Atoi(q.Get(p.k))
			if e != nil || v < 1 || v > 2147483647 {
				failure(w, 422, "Invalid input supplied", []string{fmt.Sprintf("\"%s\" must be a positive integer in query", p.k)})
				return
			}
			*p.v = v
		}
	}
	if f.Limit > 50 {
		failure(w, 422, "Invalid limit specified. Query Limit Pecked at 50", nil)
		return
	}
	switch strings.ToLower(q.Get("sort")) {
	case "", "asc", "ascending", "1":
	case "desc", "descending", "-1":
		f.Desc = true
	default:
		failure(w, 422, "Invalid input supplied", []string{`"sort" must be asc or desc in query`})
		return
	}
	if q.Has("isFlagged") {
		s := strings.ToLower(q.Get("isFlagged"))
		if s != "true" && s != "false" {
			failure(w, 422, "Invalid input supplied", []string{`"isFlagged" must be a boolean in query`})
			return
		}
		v := s == "true"
		f.Flagged = &v
	}
	rows, total, err := a.repo.ListComments(r.Context(), f)
	if err != nil {
		a.internal(w)
		return
	}
	pages := (total + f.Limit - 1) / f.Limit
	if pages == 0 {
		pages = 1
	}
	var next, prev any
	if f.Page < pages {
		next = f.Page + 1
	}
	if f.Page > 1 {
		prev = f.Page - 1
	}
	info := object{"currentPage": f.Page, "totalPages": pages, "hasNext": f.Page < pages, "hasPrev": f.Page > 1, "nextPage": next, "prevPage": prev, "pageRecordCount": len(rows), "totalRecord": total}
	if f.Page > pages {
		failure(w, 404, "Page limit exceeded, No records found!", info)
		return
	}
	records := make([]object, 0, len(rows))
	for _, v := range rows {
		records = append(records, commentData(v))
	}
	success(w, 200, object{"records": records, "pageInfo": info}, "Comments Retrieved Successfully")
}
func validateAccount(w http.ResponseWriter, b object, minKeys, emailKeys []string) bool {
	errs := []string{}
	for _, k := range minKeys {
		if utf8.RuneCountInString(str(b, k)) < 6 {
			errs = append(errs, fmt.Sprintf("\"%s\" length must be at least 6 characters long in body", k))
		}
	}
	for _, k := range emailKeys {
		s := str(b, k)
		addr, e := mail.ParseAddress(s)
		if e != nil || addr.Address != s || !strings.Contains(s, ".") {
			errs = append(errs, fmt.Sprintf("\"%s\" must be a valid email in body", k))
		}
	}
	if len(errs) > 0 {
		failure(w, 422, "Invalid input supplied", errs)
		return false
	}
	return true
}
func (a *api) createOrganization(w http.ResponseWriter, r *http.Request) {
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"organizationName", "organizationEmail", "secret", "adminName", "adminEmail", "adminPassword"}, nil, "body") {
		return
	}
	if !validateAccount(w, b, []string{"organizationName", "secret", "adminName", "adminPassword"}, []string{"organizationEmail", "adminEmail"}) {
		return
	}
	secret, err := auth.HashPassword(str(b, "secret"))
	if err != nil {
		failure(w, 400, "A error occured encrypting secret", nil)
		return
	}
	password, err := auth.HashPassword(str(b, "adminPassword"))
	if err != nil {
		failure(w, 400, "A password processing error occured", nil)
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	org := repository.Organization{ID: identifier(), Name: str(b, "organizationName"), Email: strings.ToLower(str(b, "organizationEmail")), Secret: secret, CreatedAt: now, UpdatedAt: now}
	admin := repository.Admin{ID: identifier(), OrganizationID: org.ID, Fullname: str(b, "adminName"), Email: strings.ToLower(str(b, "adminEmail")), Password: password, CreatedAt: now, UpdatedAt: now}
	token, err := a.tokens.SignOrganization(org.ID, admin.ID)
	if err != nil {
		a.internal(w)
		return
	}
	err = a.repo.CreateOrganization(r.Context(), org, admin)
	if errors.Is(err, repository.ErrConflict) {
		failure(w, 400, "An error occured creating organization: organization email already exists", nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 201, object{"organizationId": org.ID, "adminId": admin.ID, "organizationToken": token}, "Organization and default admin account created successfully")
}
func (a *api) organizationToken(w http.ResponseWriter, r *http.Request) {
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"organizationId", "email", "password"}, nil, "body") {
		return
	}
	if !validateAccount(w, b, []string{"password"}, []string{"email"}) {
		return
	}
	if !validID(str(b, "organizationId")) {
		failure(w, 422, "Invalid input supplied", []string{`"organizationId" must be a valid ObjectId in body`})
		return
	}
	admin, err := a.repo.FindAdmin(r.Context(), str(b, "organizationId"), strings.ToLower(str(b, "email")))
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		a.internal(w)
		return
	}
	// A fixed valid hash makes nonexistent-account checks take bcrypt work too.
	hash := admin.Password
	if err != nil {
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5ql/6jK7i9XQ17Aqj4LHu6hVvzZ6yAa"
	}
	passErr := auth.ComparePassword(hash, str(b, "password"))
	if err != nil || passErr != nil {
		failure(w, 401, "Invalid email and/or password", nil)
		return
	}
	if _, err = a.repo.GetOrganization(r.Context(), admin.OrganizationID); err != nil {
		a.authLookupError(w, err)
		return
	}
	token, err := a.tokens.SignOrganization(admin.OrganizationID, admin.ID)
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 200, object{"organizationToken": token}, "Organization token generated successfully")
}
func (a *api) createApplication(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "organization")
	if !ok {
		return
	}
	b, ok := a.body(w, r)
	if !ok || !validateStrings(w, b, []string{"name"}, nil, "body") {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	v := repository.Application{ID: identifier(), OrganizationID: c.OrganizationID, CreatedBy: c.AdminID, Name: str(b, "name"), CreatedAt: now, UpdatedAt: now}
	token, err := a.tokens.SignApplication(v.ID, c.AdminID)
	if err != nil {
		a.internal(w)
		return
	}
	err = a.repo.CreateApplication(r.Context(), v)
	if errors.Is(err, repository.ErrConflict) {
		failure(w, 400, fmt.Sprintf("An error occured creating application. Error: : Application with name: %s already exists", v.Name), nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 201, object{"applicationId": v.ID, "applicationToken": token}, "Application created successfully")
}
func (a *api) applicationToken(w http.ResponseWriter, r *http.Request) {
	c, ok := a.claims(w, r, "organization")
	if !ok {
		return
	}
	id := r.PathValue("applicationId")
	if !validID(id) {
		failure(w, 422, "Invalid input supplied", []string{`"applicationId" must be a valid ObjectId in params`})
		return
	}
	v, err := a.repo.GetApplication(r.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		failure(w, 400, "Invalid application ID", nil)
		return
	}
	if err != nil {
		a.internal(w)
		return
	}
	if v.OrganizationID != c.OrganizationID {
		failure(w, 401, "You are not authorized to access this resource", nil)
		return
	}
	token, err := a.tokens.SignApplication(id, c.AdminID)
	if err != nil {
		a.internal(w)
		return
	}
	success(w, 201, object{"applicationToken": token}, "successfully generated token")
}
