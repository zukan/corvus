package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryCommentOwnershipAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	org := Organization{ID: "org-1", Name: "Org", Email: "org@example.com"}
	admin := Admin{ID: "admin-1", OrganizationID: org.ID, Email: "admin@example.com"}
	if err := r.CreateOrganization(ctx, org, admin); err != nil {
		t.Fatal(err)
	}
	app := Application{ID: "app-1", OrganizationID: org.ID, Name: "App"}
	if err := r.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	comment := Comment{ID: "c-1", ApplicationID: app.ID, OwnerID: "owner", Content: "before", CreatedAt: time.Now().Add(-time.Minute)}
	if err := r.CreateComment(ctx, comment); err != nil {
		t.Fatal(err)
	}
	got, err := r.UpdateComment(ctx, app.ID, comment.ID, "owner", "after")
	if err != nil || got.Content != "after" {
		t.Fatalf("update = %#v, %v", got, err)
	}
	if _, err := r.UpdateComment(ctx, app.ID, comment.ID, "other", "bad"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner error = %v", err)
	}
	beforeDelete, err := r.DeleteComment(ctx, app.ID, comment.ID, "owner")
	if err != nil || beforeDelete.Deleted || beforeDelete.Content != "after" {
		t.Fatalf("delete return = %#v, %v", beforeDelete, err)
	}
	if _, err := r.GetComment(ctx, app.ID, comment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
}

func TestMemoryListCommentsFiltersAndStablePaging(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	if err := r.CreateOrganization(ctx, Organization{ID: "o", Name: "n"}, Admin{ID: "a", OrganizationID: "o"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateApplication(ctx, Application{ID: "app", OrganizationID: "o"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Comment{{ID: "b", ApplicationID: "app", OwnerID: "u", Content: "b"}, {ID: "a", ApplicationID: "app", OwnerID: "u", Content: "a"}, {ID: "c", ApplicationID: "app", OwnerID: "v", Flags: []string{"x"}}} {
		if err := r.CreateComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	owner := "u"
	items, total, err := r.ListComments(ctx, Filter{ApplicationID: "app", OwnerID: &owner, Page: 1, Limit: 1})
	if err != nil || total != 2 || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("page = %#v total=%d err=%v", items, total, err)
	}
	desc, _, err := r.ListComments(ctx, Filter{ApplicationID: "app", OwnerID: &owner, Page: 1, Limit: 2, Desc: true})
	if err != nil || len(desc) != 2 || desc[0].ID != "b" {
		t.Fatalf("descending = %#v err=%v", desc, err)
	}
}

func TestMemoryConcurrentCommentWritesRemainConsistent(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	if err := r.CreateOrganization(ctx, Organization{ID: "concurrent-org", Name: "N", Email: "concurrent@example.com"}, Admin{ID: "concurrent-admin", OrganizationID: "concurrent-org", Email: "admin-concurrent@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateApplication(ctx, Application{ID: "concurrent-app", OrganizationID: "concurrent-org"}); err != nil {
		t.Fatal(err)
	}
	const count = 32
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- r.CreateComment(ctx, Comment{ID: fmt.Sprintf("comment-%02d", i), ApplicationID: "concurrent-app", OwnerID: "owner", Content: "body"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := r.ListComments(ctx, Filter{ApplicationID: "concurrent-app", Page: 1, Limit: count})
	if err != nil {
		t.Fatal(err)
	}
	if total != count || len(items) != count {
		t.Fatalf("list returned %d of %d", len(items), total)
	}
}

func TestMemoryApplicationNamesAreUniquePerOrganizationAndCaseSensitive(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	if err := r.CreateOrganization(ctx, Organization{ID: "name-org", Name: "Org", Email: "name-org@example.com"}, Admin{ID: "name-admin", OrganizationID: "name-org", Email: "name-admin@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateApplication(ctx, Application{ID: "name-a", OrganizationID: "name-org", Name: "Console"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateApplication(ctx, Application{ID: "name-b", OrganizationID: "name-org", Name: "Console"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same tenant name = %v", err)
	}
	if err := r.CreateApplication(ctx, Application{ID: "name-c", OrganizationID: "name-org", Name: "console"}); err != nil {
		t.Fatalf("case-sensitive name = %v", err)
	}
}

func TestMemoryImportedDeletedAtPreservesValuesAndSoftDeleteSetsTimestamps(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	sourceTime := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.FixedZone("source", 9*60*60))
	org := Organization{ID: "deleted-at-org", Name: "Org", Email: "deleted-at-org@example.com", Deleted: true, DeletedAt: &sourceTime}
	if inserted, err := r.PutImported(ctx, "organizations", org); err != nil || !inserted {
		t.Fatalf("organization import = %v,%v", inserted, err)
	}
	app := Application{ID: "deleted-at-app", OrganizationID: org.ID, Name: "App", Deleted: true, DeletedAt: &sourceTime}
	if inserted, err := r.PutImported(ctx, "applications", app); err != nil || !inserted {
		t.Fatalf("application import = %v,%v", inserted, err)
	}
	if got, err := r.GetOrganization(ctx, org.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted organization lookup = %#v,%v", got, err)
	}
	expectedOrgTime := sourceTime.UTC().Truncate(time.Millisecond)
	if got := r.orgs[org.ID].DeletedAt; got == nil || !got.Equal(expectedOrgTime) {
		t.Fatalf("organization import deleted_at = %v", got)
	}
	if got := r.apps[app.ID].DeletedAt; got == nil || !got.Equal(expectedOrgTime) {
		t.Fatalf("application import deleted_at = %v", got)
	}
	activeApp := Application{ID: "active-deleted-at-app", OrganizationID: org.ID, Name: "Active"}
	if err := r.CreateApplication(ctx, activeApp); err != nil {
		t.Fatal(err)
	}
	commentTime := sourceTime.Add(time.Hour).Truncate(time.Millisecond)
	imported := Comment{ID: "imported-deleted-at-comment", ApplicationID: activeApp.ID, OwnerID: "owner", Content: "historical", Deleted: true, DeletedAt: &commentTime}
	if inserted, err := r.PutImported(ctx, "comments", imported); err != nil || !inserted {
		t.Fatalf("comment import = %v,%v", inserted, err)
	}
	storedImported := r.comments[imported.ID]
	if storedImported.DeletedAt == nil || !storedImported.DeletedAt.Equal(commentTime.UTC()) {
		t.Fatalf("imported comment deleted_at = %v", storedImported.DeletedAt)
	}
	comment := Comment{ID: "active-deleted-at-comment", ApplicationID: activeApp.ID, OwnerID: "owner", Content: "body"}
	if err := r.CreateComment(ctx, comment); err != nil {
		t.Fatal(err)
	}
	reply := Reply{ID: "deleted-at-reply", CommentID: comment.ID, OwnerID: "owner", Content: "reply", DeletedAt: &commentTime}
	if inserted, err := r.PutImported(ctx, "replies", reply); err != nil || !inserted {
		t.Fatalf("reply import = %v,%v", inserted, err)
	}
	storedReply := r.replies[reply.ID]
	if storedReply.DeletedAt == nil || !storedReply.DeletedAt.Equal(commentTime.UTC()) {
		t.Fatalf("imported reply deleted_at = %v", storedReply.DeletedAt)
	}
	gotDeleted, err := r.DeleteComment(ctx, activeApp.ID, comment.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if gotDeleted.DeletedAt != nil {
		t.Fatalf("pre-delete response deleted_at = %v", gotDeleted.DeletedAt)
	}
	stored := r.comments[comment.ID]
	storedReply = r.replies[reply.ID]
	if stored.DeletedAt == nil || storedReply.DeletedAt == nil || stored.DeletedAt.IsZero() || storedReply.DeletedAt.IsZero() {
		t.Fatalf("soft delete timestamps comment=%v reply=%v", stored.DeletedAt, storedReply.DeletedAt)
	}
}
