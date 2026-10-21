//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationRepositoryFlows(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured")
	}
	ctx := context.Background()
	r, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	orgID, appID, otherOrgID, otherAppID := "org-"+suffix, "app-"+suffix, "other-org-"+suffix, "other-app-"+suffix
	if err = r.CreateOrganization(ctx, Organization{ID: orgID, Name: "Example", Email: "org-" + suffix + "@example.com"}, Admin{ID: "admin-" + suffix, OrganizationID: orgID, Email: "admin-" + suffix + "@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err = r.CreateOrganization(ctx, Organization{ID: otherOrgID, Name: "Other", Email: "other-" + suffix + "@example.com"}, Admin{ID: "other-admin-" + suffix, OrganizationID: otherOrgID, Email: "other-admin-" + suffix + "@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err = r.CreateApplication(ctx, Application{ID: appID, OrganizationID: orgID, CreatedBy: "admin-" + suffix, Name: "App"}); err != nil {
		t.Fatal(err)
	}
	if err = r.CreateApplication(ctx, Application{ID: otherAppID, OrganizationID: otherOrgID, CreatedBy: "other-admin-" + suffix, Name: "Other app"}); err != nil {
		t.Fatal(err)
	}
	if err = r.CreateApplication(ctx, Application{ID: "duplicate-app-" + suffix, OrganizationID: orgID, Name: "App"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate organization application name = %v", err)
	}
	sourceDeletedAt := time.Date(2022, 5, 6, 7, 8, 9, 765432100, time.FixedZone("source", 9*60*60))
	expectedDeletedAt := sourceDeletedAt.UTC().Truncate(time.Millisecond)
	deletedOrg := Organization{ID: "deleted-org-" + suffix, Name: "Archived", Email: "deleted-org-" + suffix + "@example.com", Deleted: true, DeletedAt: &sourceDeletedAt}
	if inserted, importErr := r.PutImported(ctx, "organizations", deletedOrg); importErr != nil || !inserted {
		t.Fatalf("deleted organization import = %v, %v", inserted, importErr)
	}
	deletedApp := Application{ID: "deleted-app-" + suffix, OrganizationID: deletedOrg.ID, Name: "Archived app", Deleted: true, DeletedAt: &sourceDeletedAt}
	if inserted, importErr := r.PutImported(ctx, "applications", deletedApp); importErr != nil || !inserted {
		t.Fatalf("deleted application relation import = %v, %v", inserted, importErr)
	}
	var archivedOrg string
	var archivedAppDeleted bool
	var archivedAppDeletedAt time.Time
	if err = r.db.QueryRowContext(ctx, "SELECT organization_id,deleted,deleted_at FROM applications WHERE id=?", deletedApp.ID).Scan(&archivedOrg, &archivedAppDeleted, &archivedAppDeletedAt); err != nil {
		t.Fatal(err)
	}
	if archivedOrg != deletedOrg.ID || !archivedAppDeleted || !archivedAppDeletedAt.Equal(expectedDeletedAt) {
		t.Fatalf("deleted relation was not preserved: org=%s deleted=%v deleted_at=%v", archivedOrg, archivedAppDeleted, archivedAppDeletedAt)
	}
	var archivedOrgDeletedAt time.Time
	if err = r.db.QueryRowContext(ctx, "SELECT deleted_at FROM organizations WHERE id=?", deletedOrg.ID).Scan(&archivedOrgDeletedAt); err != nil {
		t.Fatal(err)
	}
	if !archivedOrgDeletedAt.Equal(expectedDeletedAt) {
		t.Fatalf("organization deleted_at = %v want %v", archivedOrgDeletedAt, expectedDeletedAt)
	}
	if err = r.CreateApplication(ctx, Application{ID: "missing-app-" + suffix, OrganizationID: "missing-org-" + suffix}); err == nil {
		t.Fatal("application with missing organization should fail")
	}
	created := time.UnixMilli(1_700_000_000_000).UTC()
	ref, origin := "ref-"+suffix, "origin-"+suffix
	flags := []string{"needs-review"}
	a := Comment{ID: "comment-a-" + suffix, ApplicationID: appID, OwnerID: "owner", Content: "first", RefID: &ref, Origin: &origin, Flags: flags, CreatedAt: created, UpdatedAt: created}
	b := Comment{ID: "comment-b-" + suffix, ApplicationID: appID, OwnerID: "owner", Content: "second", RefID: &ref, Origin: &origin, CreatedAt: created, UpdatedAt: created}
	if err = r.CreateComment(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = r.CreateComment(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetComment(ctx, otherAppID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross application get = %v", err)
	}
	ownerFilter, refFilter, flagFilter := "owner", ref, true
	items, total, err := r.ListComments(ctx, Filter{ApplicationID: appID, OwnerID: &ownerFilter, RefID: &refFilter, Flagged: &flagFilter, Page: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != a.ID || len(items[0].Flags) != 1 {
		t.Fatalf("filtered page = %#v total=%d", items, total)
	}
	items, total, err = r.ListComments(ctx, Filter{ApplicationID: appID, OwnerID: &ownerFilter, RefID: &refFilter, Page: 2, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 1 || items[0].ID != b.ID {
		t.Fatalf("stable second page = %#v total=%d", items, total)
	}
	if _, err = r.UpdateComment(ctx, appID, a.ID, "Owner", "forbidden"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong owner update = %v", err)
	}
	if _, err = r.UpdateComment(ctx, otherAppID, a.ID, "owner", "cross-tenant"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross application update = %v", err)
	}
	updated, err := r.UpdateComment(ctx, appID, a.ID, "owner", "changed")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "changed" || !updated.UpdatedAt.Equal(created) {
		t.Fatalf("legacy update response = %#v", updated)
	}
	saved, err := r.GetComment(ctx, appID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Content != "changed" || !saved.UpdatedAt.After(created) {
		t.Fatalf("persisted update = %#v", saved)
	}
	imported := Comment{ID: "imported-" + suffix, ApplicationID: appID, OwnerID: "import-owner", Content: "imported", Replies: []string{"reply-id"}, Flags: []string{"flag"}, UpVotes: []string{"up"}, DownVotes: []string{"down"}, CreatedAt: created, UpdatedAt: created, Deleted: true, DeletedAt: &sourceDeletedAt}
	inserted, err := r.PutImported(ctx, "comments", imported)
	if err != nil || !inserted {
		t.Fatalf("first import = %v, %v", inserted, err)
	}
	changedImport := imported
	changedImport.Content = "must not overwrite"
	inserted, err = r.PutImported(ctx, "comments", changedImport)
	if err != nil || inserted {
		t.Fatalf("repeat import = %v, %v", inserted, err)
	}
	var importedContent string
	var importedIsDeleted bool
	var importedDeletedAt sql.NullTime
	var repliesJSON, flagsJSON, upsJSON, downsJSON []byte
	if err = r.db.QueryRowContext(ctx, "SELECT content,replies,flags,up_votes,down_votes,deleted,deleted_at FROM comments WHERE application_id=? AND id=?", appID, imported.ID).Scan(&importedContent, &repliesJSON, &flagsJSON, &upsJSON, &downsJSON, &importedIsDeleted, &importedDeletedAt); err != nil {
		t.Fatal(err)
	}
	if importedContent != "imported" || !importedIsDeleted || !importedDeletedAt.Valid || !importedDeletedAt.Time.Equal(expectedDeletedAt) {
		t.Fatalf("imported record content=%q deleted=%v deleted_at=%v", importedContent, importedIsDeleted, importedDeletedAt)
	}
	if len(decodeStrings(repliesJSON)) != 1 || len(decodeStrings(upsJSON)) != 1 {
		t.Fatalf("imported arrays were not preserved: replies=%s ups=%s flags=%s downs=%s", repliesJSON, upsJSON, flagsJSON, downsJSON)
	}
	if _, err = r.PutImported(ctx, "replies", Reply{ID: "orphan-" + suffix, CommentID: "absent-" + suffix, OwnerID: "owner"}); err == nil {
		t.Fatal("orphan reply import should fail")
	}
	reply := Reply{ID: "reply-" + suffix, CommentID: a.ID, OwnerID: "owner", Content: "reply", Flags: []string{"rf"}, UpVotes: []string{"ru"}, DownVotes: []string{"rd"}, CreatedAt: created, UpdatedAt: created, DeletedAt: &sourceDeletedAt}
	if inserted, err = r.PutImported(ctx, "replies", reply); err != nil || !inserted {
		t.Fatalf("reply import = %v, %v", inserted, err)
	}
	var importedReplyDeletedAt time.Time
	if err = r.db.QueryRowContext(ctx, "SELECT deleted_at FROM comment_replies WHERE id=?", reply.ID).Scan(&importedReplyDeletedAt); err != nil {
		t.Fatal(err)
	}
	if !importedReplyDeletedAt.Equal(expectedDeletedAt) {
		t.Fatalf("reply imported deleted_at=%v want=%v", importedReplyDeletedAt, expectedDeletedAt)
	}
	if _, err = r.DeleteComment(ctx, otherAppID, a.ID, "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross application delete = %v", err)
	}
	deleted, err := r.DeleteComment(ctx, appID, a.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Deleted || deleted.Content != "changed" || !deleted.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatalf("legacy delete response = %#v", deleted)
	}
	if _, err = r.GetComment(ctx, appID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted comment get = %v", err)
	}
	var commentDeleted, replyDeleted bool
	var commentDeletedAt, replyDeletedAt sql.NullTime
	if err = r.db.QueryRowContext(ctx, "SELECT deleted,deleted_at FROM comments WHERE id=?", a.ID).Scan(&commentDeleted, &commentDeletedAt); err != nil {
		t.Fatal(err)
	}
	if err = r.db.QueryRowContext(ctx, "SELECT deleted,deleted_at FROM comment_replies WHERE id=?", reply.ID).Scan(&replyDeleted, &replyDeletedAt); err != nil {
		t.Fatal(err)
	}
	if !commentDeleted || !replyDeleted || !commentDeletedAt.Valid || !replyDeletedAt.Valid || !commentDeletedAt.Time.Equal(replyDeletedAt.Time) || !commentDeletedAt.Time.After(saved.UpdatedAt) {
		t.Fatalf("soft delete state comment=%v at=%v reply=%v at=%v", commentDeleted, commentDeletedAt, replyDeleted, replyDeletedAt)
	}
}
