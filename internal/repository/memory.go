package repository

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Memory struct {
	mu       sync.RWMutex
	orgs     map[string]Organization
	admins   map[string]Admin
	apps     map[string]Application
	comments map[string]Comment
	replies  map[string]Reply
}

func NewMemory() *Memory {
	return &Memory{orgs: map[string]Organization{}, admins: map[string]Admin{}, apps: map[string]Application{}, comments: map[string]Comment{}, replies: map[string]Reply{}}
}
func check(ctx context.Context) error               { return ctx.Err() }
func (m *Memory) Ping(ctx context.Context) error    { return check(ctx) }
func (m *Memory) Ready(ctx context.Context) error   { return check(ctx) }
func (m *Memory) Close() error                      { return nil }
func (m *Memory) Migrate(ctx context.Context) error { return check(ctx) }
func (m *Memory) CreateOrganization(ctx context.Context, o Organization, a Admin) error {
	if e := check(ctx); e != nil {
		return e
	}
	if err := validateOrganization(o); err != nil {
		return err
	}
	if err := validateAdmin(a); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.ID == "" || a.ID == "" || a.OrganizationID != o.ID {
		return ErrConflict
	}
	if _, ok := m.orgs[o.ID]; ok {
		return ErrConflict
	}
	for _, x := range m.orgs {
		if x.Email == o.Email {
			return ErrConflict
		}
	}
	for _, x := range m.admins {
		if x.OrganizationID == a.OrganizationID && x.Email == a.Email {
			return ErrConflict
		}
	}
	o.CreatedAt = stamp(o.CreatedAt)
	o.UpdatedAt = stamp(o.UpdatedAt)
	o.DeletedAt = cloneTime(o.DeletedAt)
	a.CreatedAt = stamp(a.CreatedAt)
	a.UpdatedAt = stamp(a.UpdatedAt)
	m.orgs[o.ID] = o
	m.admins[a.ID] = a
	return nil
}
func (m *Memory) GetOrganization(ctx context.Context, id string) (Organization, error) {
	if e := check(ctx); e != nil {
		return Organization{}, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.orgs[id]
	v.DeletedAt = cloneTime(v.DeletedAt)
	if !ok || v.Deleted {
		return Organization{}, ErrNotFound
	}
	return v, nil
}
func (m *Memory) FindAdmin(ctx context.Context, org, email string) (Admin, error) {
	if e := check(ctx); e != nil {
		return Admin{}, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.admins {
		if a.OrganizationID == org && a.Email == email {
			return a, nil
		}
	}
	return Admin{}, ErrNotFound
}
func (m *Memory) GetAdmin(ctx context.Context, id string) (Admin, error) {
	if e := check(ctx); e != nil {
		return Admin{}, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.admins[id]
	if !ok {
		return Admin{}, ErrNotFound
	}
	return v, nil
}
func (m *Memory) CreateApplication(ctx context.Context, a Application) error {
	if e := check(ctx); e != nil {
		return e
	}
	if err := validateApplication(a); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[a.OrganizationID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.apps[a.ID]; ok {
		return ErrConflict
	}
	for _, old := range m.apps {
		if old.OrganizationID == a.OrganizationID && old.Name == a.Name {
			return ErrConflict
		}
	}
	a.CreatedAt = stamp(a.CreatedAt)
	a.UpdatedAt = stamp(a.UpdatedAt)
	a.DeletedAt = cloneTime(a.DeletedAt)
	m.apps[a.ID] = a
	return nil
}
func (m *Memory) GetApplication(ctx context.Context, id string) (Application, error) {
	if e := check(ctx); e != nil {
		return Application{}, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.apps[id]
	v.DeletedAt = cloneTime(v.DeletedAt)
	if !ok || v.Deleted {
		return Application{}, ErrNotFound
	}
	return v, nil
}
func (m *Memory) CreateComment(ctx context.Context, c Comment) error {
	if e := check(ctx); e != nil {
		return e
	}
	if err := validateComment(c); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.apps[c.ApplicationID]
	if !ok || a.Deleted {
		return ErrNotFound
	}
	if _, ok = m.comments[c.ID]; ok {
		return ErrConflict
	}
	c.CreatedAt = stamp(c.CreatedAt)
	c.UpdatedAt = stamp(c.UpdatedAt)
	c.DeletedAt = cloneTime(c.DeletedAt)
	m.comments[c.ID] = cloneComment(c)
	return nil
}
func (m *Memory) GetComment(ctx context.Context, app, id string) (Comment, error) {
	if e := check(ctx); e != nil {
		return Comment{}, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.comments[id]
	if !ok || c.ApplicationID != app || c.Deleted {
		return Comment{}, ErrNotFound
	}
	return cloneComment(c), nil
}
func (m *Memory) ListComments(ctx context.Context, f Filter) ([]Comment, int, error) {
	if e := check(ctx); e != nil {
		return nil, 0, e
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := []Comment{}
	for _, c := range m.comments {
		if c.ApplicationID != f.ApplicationID || c.Deleted {
			continue
		}
		if f.RefID != nil && (c.RefID == nil || *c.RefID != *f.RefID) {
			continue
		}
		if f.Origin != nil && (c.Origin == nil || *c.Origin != *f.Origin) {
			continue
		}
		if f.OwnerID != nil && c.OwnerID != *f.OwnerID {
			continue
		}
		if f.Flagged != nil && ((len(c.Flags) > 0) != *f.Flagged) {
			continue
		}
		all = append(all, cloneComment(c))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			if f.Desc {
				return all[i].ID > all[j].ID
			}
			return all[i].ID < all[j].ID
		}
		if f.Desc {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	n := len(all)
	p, l := f.Page, f.Limit
	if p < 1 {
		p = 1
	}
	if l < 1 {
		l = 50
	}
	start := (p - 1) * l
	if start >= n {
		return []Comment{}, n, nil
	}
	end := start + l
	if end > n {
		end = n
	}
	return all[start:end], n, nil
}
func (m *Memory) UpdateComment(ctx context.Context, app, id, owner, content string) (Comment, error) {
	if e := check(ctx); e != nil {
		return Comment{}, e
	}
	if err := validateText("comment content", content, maxMediumTextBytes); err != nil {
		return Comment{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.comments[id]
	if !ok || c.ApplicationID != app || c.Deleted {
		return Comment{}, ErrNotFound
	}
	if c.OwnerID != owner {
		return Comment{}, ErrForbidden
	}
	previous := cloneComment(c)
	c.Content = content
	c.UpdatedAt = stamp(time.Now())
	m.comments[id] = c
	previous.Content = content
	return previous, nil
}
func (m *Memory) DeleteComment(ctx context.Context, app, id, owner string) (Comment, error) {
	if e := check(ctx); e != nil {
		return Comment{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.comments[id]
	if !ok || c.ApplicationID != app || c.Deleted {
		return Comment{}, ErrNotFound
	}
	if c.OwnerID != owner {
		return Comment{}, ErrForbidden
	}
	out := cloneComment(c)
	now := stamp(time.Now())
	c.Deleted = true
	c.DeletedAt = &now
	c.UpdatedAt = now
	m.comments[id] = c
	for rid, r := range m.replies {
		if r.CommentID == id && !r.Deleted {
			r.Deleted = true
			t := c.UpdatedAt
			r.DeletedAt = &t
			r.UpdatedAt = c.UpdatedAt
			m.replies[rid] = r
		}
	}
	out.Deleted = false
	return out, nil
}
func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Millisecond)
	return &v
}
func cloneComment(c Comment) Comment {
	c.DeletedAt = cloneTime(c.DeletedAt)
	c.Replies = append([]string(nil), c.Replies...)
	c.Flags = append([]string(nil), c.Flags...)
	c.UpVotes = append([]string(nil), c.UpVotes...)
	c.DownVotes = append([]string(nil), c.DownVotes...)
	if c.RefID != nil {
		x := *c.RefID
		c.RefID = &x
	}
	if c.Origin != nil {
		x := *c.Origin
		c.Origin = &x
	}
	return c
}
func (m *Memory) PutImported(ctx context.Context, kind string, value any) (bool, error) {
	if err := ValidateImported(kind, value); err != nil {
		return false, err
	}
	if e := check(ctx); e != nil {
		return false, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch kind {
	case "organizations":
		o, ok := value.(Organization)
		if !ok {
			return false, ErrConflict
		}
		if err := validateOrganization(o); err != nil {
			return false, err
		}
		if _, exists := m.orgs[o.ID]; exists {
			return false, nil
		}
		for _, x := range m.orgs {
			if x.Email == o.Email {
				return false, ErrConflict
			}
		}
		o.DeletedAt = cloneTime(o.DeletedAt)
		m.orgs[o.ID] = o
		return true, nil
	case "admins":
		a, ok := value.(Admin)
		if !ok {
			return false, ErrConflict
		}
		if err := validateAdmin(a); err != nil {
			return false, err
		}
		if _, exists := m.admins[a.ID]; exists {
			return false, nil
		}
		if _, ok = m.orgs[a.OrganizationID]; !ok {
			return false, ErrNotFound
		}
		for _, x := range m.admins {
			if x.OrganizationID == a.OrganizationID && x.Email == a.Email {
				return false, ErrConflict
			}
		}
		m.admins[a.ID] = a
		return true, nil
	case "applications":
		a, ok := value.(Application)
		if !ok {
			return false, ErrConflict
		}
		if err := validateApplication(a); err != nil {
			return false, err
		}
		if _, exists := m.apps[a.ID]; exists {
			return false, nil
		}
		for _, old := range m.apps {
			if old.OrganizationID == a.OrganizationID && old.Name == a.Name {
				return false, ErrConflict
			}
		}
		if _, ok = m.orgs[a.OrganizationID]; !ok {
			return false, ErrNotFound
		}
		a.DeletedAt = cloneTime(a.DeletedAt)
		m.apps[a.ID] = a
		return true, nil
	case "comments":
		c, ok := value.(Comment)
		if !ok {
			return false, ErrConflict
		}
		if err := validateComment(c); err != nil {
			return false, err
		}
		if _, exists := m.comments[c.ID]; exists {
			return false, nil
		}
		if _, ok = m.apps[c.ApplicationID]; !ok {
			return false, ErrNotFound
		}
		m.comments[c.ID] = cloneComment(c)
		return true, nil
	case "replies":
		r, ok := value.(Reply)
		if !ok {
			return false, ErrConflict
		}
		if err := validateReply(r); err != nil {
			return false, err
		}
		if _, exists := m.replies[r.ID]; exists {
			return false, nil
		}
		if _, ok = m.comments[r.CommentID]; !ok {
			return false, ErrNotFound
		}
		r.DeletedAt = cloneTime(r.DeletedAt)
		m.replies[r.ID] = r
		return true, nil
	}
	return false, ErrNotFound
}

var _ Repository = (*Memory)(nil)
