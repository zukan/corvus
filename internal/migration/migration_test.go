package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zukan/corvus/internal/repository"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"
)

func testBcryptHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("test-only"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

type fakeSource map[string][]bson.M

func (f fakeSource) Iterate(ctx context.Context, collection string, visit func(bson.M) error) error {
	for _, doc := range f[collection] {
		if err := visit(doc); err != nil {
			return err
		}
	}
	return nil
}

type fakeSink struct {
	migrated   int
	rows       map[string]map[string]any
	putError   error
	putErrorAt int
	putCalls   int
}

func (f *fakeSink) Migrate(context.Context) error { f.migrated++; return nil }
func (f *fakeSink) PutImported(_ context.Context, kind string, value any) (bool, error) {
	f.putCalls++
	errorAt := f.putErrorAt
	if errorAt == 0 {
		errorAt = 2
	}
	if f.putError != nil && f.putCalls == errorAt {
		return false, f.putError
	}
	if f.rows == nil {
		f.rows = map[string]map[string]any{}
	}
	if f.rows[kind] == nil {
		f.rows[kind] = map[string]any{}
	}
	id := ""
	switch v := value.(type) {
	case repository.Organization:
		id = v.ID
	case repository.Admin:
		id = v.ID
	case repository.Application:
		id = v.ID
	case repository.Comment:
		id = v.ID
	case repository.Reply:
		id = v.ID
	}
	if _, ok := f.rows[kind][id]; ok {
		return false, nil
	}
	f.rows[kind][id] = value
	return true, nil
}

func validImportSource(t *testing.T) fakeSource {
	t.Helper()
	now := time.Date(2023, 7, 8, 9, 10, 11, 0, time.UTC)
	return fakeSource{
		"organizations": {{"_id": "source-org", "name": "Source Org", "email": "org@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now}},
		"admins":        {{"_id": "source-admin", "organizationId": "source-org", "fullname": "Source Admin", "email": "admin@example.invalid", "password": testBcryptHash(t), "createdAt": now, "updatedAt": now}},
		"applications":  {{"_id": "shared-app", "organizationId": "source-org", "createdBy": "source-admin", "name": "Source App", "createdAt": now, "updatedAt": now}},
		"comments":      {{"_id": "source-comment", "applicationId": "shared-app", "ownerId": "owner", "content": "Source comment", "createdAt": now, "updatedAt": now}},
		"replies":       {{"_id": "source-reply", "commentId": "source-comment", "ownerId": "owner", "content": "Source reply", "createdAt": now, "updatedAt": now}},
	}
}

func TestRunStopsBeforeChildrenOnTargetBindingConflict(t *testing.T) {
	existing := repository.Application{ID: "shared-app", OrganizationID: "other-org", CreatedBy: "other-admin", Name: "Existing App"}
	sink := &fakeSink{
		rows:     map[string]map[string]any{"applications": {existing.ID: existing}},
		putError: repository.ErrConflict, putErrorAt: 3,
	}
	report, err := Run(context.Background(), validImportSource(t), sink, false)
	if err == nil || err.Error() != "insert applications record 1 failed" {
		t.Fatalf("expected sanitized application location, got %v", err)
	}
	if sink.putCalls != 3 || len(sink.rows["comments"]) != 0 || len(sink.rows["replies"]) != 0 {
		t.Fatal("target conflict allowed child inserts")
	}
	if sink.rows["applications"][existing.ID] != existing {
		t.Fatal("target application was overwritten")
	}
	if report["applications"].Skipped != 0 || report["applications"].Inserted != 0 {
		t.Fatal("target conflict was counted as success")
	}
}

func TestRunRejectsOrphanBeforeAnyWrites(t *testing.T) {
	now := time.Now()
	orgID := bson.NewObjectID()
	source := fakeSource{
		"organizations": {{"_id": orgID, "name": "n", "email": "e", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now}},
		"admins":        {{"_id": bson.NewObjectID(), "organizationId": bson.NewObjectID(), "fullname": "f", "email": "e", "password": testBcryptHash(t), "createdAt": now, "updatedAt": now}},
	}
	sink := &fakeSink{}
	counts, err := Run(context.Background(), source, sink, false)
	if err == nil || !strings.Contains(err.Error(), "admins record 1") {
		t.Fatalf("expected sanitized orphan location, got counts=%#v err=%v", counts, err)
	}
	if sink.migrated != 0 || sink.putCalls != 0 || len(sink.rows) != 0 {
		t.Fatalf("orphan preflight allowed writes: migrated=%d puts=%d rows=%#v", sink.migrated, sink.putCalls, sink.rows)
	}
}

func TestRunRejectsEveryOrphanParentType(t *testing.T) {
	now := time.Now()
	orgID, otherOrgID, adminID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	org := bson.M{"_id": orgID, "name": "n", "email": "org@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now}
	admin := bson.M{"_id": adminID, "organizationId": orgID, "fullname": "f", "email": "admin@example.invalid", "password": testBcryptHash(t), "createdAt": now, "updatedAt": now}
	tests := []struct {
		name       string
		source     fakeSource
		collection string
	}{
		{
			name:       "admin organization",
			source:     fakeSource{"admins": {{"_id": bson.NewObjectID(), "organizationId": otherOrgID, "fullname": "f", "email": "admin@example.invalid", "password": testBcryptHash(t), "createdAt": now, "updatedAt": now}}},
			collection: "admins",
		},
		{
			name:       "application organization",
			source:     fakeSource{"organizations": {org}, "admins": {admin}, "applications": {{"_id": bson.NewObjectID(), "organizationId": otherOrgID, "createdBy": adminID, "name": "app", "createdAt": now, "updatedAt": now}}},
			collection: "applications",
		},
		{
			name:       "comment application",
			source:     fakeSource{"comments": {{"_id": bson.NewObjectID(), "applicationId": bson.NewObjectID(), "ownerId": "owner", "content": "body", "createdAt": now, "updatedAt": now}}},
			collection: "comments",
		},
		{
			name:       "reply comment",
			source:     fakeSource{"replies": {{"_id": bson.NewObjectID(), "commentId": bson.NewObjectID(), "ownerId": "owner", "content": "reply", "createdAt": now, "updatedAt": now}}},
			collection: "replies",
		},
		{
			name:       "application creator",
			source:     fakeSource{"organizations": {org}, "applications": {{"_id": bson.NewObjectID(), "organizationId": orgID, "createdBy": bson.NewObjectID(), "name": "app", "createdAt": now, "updatedAt": now}}},
			collection: "applications",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counts, err := Run(context.Background(), tt.source, &fakeSink{}, true)
			if err == nil || !strings.Contains(err.Error(), tt.collection+" record 1") {
				t.Fatalf("expected sanitized %s parent location, got counts=%#v err=%v", tt.collection, counts, err)
			}
		})
	}
}

func TestRunRejectsApplicationCreatedByAnotherTenant(t *testing.T) {
	now := time.Now()
	orgA, orgB, adminB := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	source := fakeSource{
		"organizations": {
			{"_id": orgA, "name": "a", "email": "a@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now},
			{"_id": orgB, "name": "b", "email": "b@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now},
		},
		"admins":       {{"_id": adminB, "organizationId": orgB, "fullname": "b", "email": "b-admin@example.invalid", "password": testBcryptHash(t), "createdAt": now, "updatedAt": now}},
		"applications": {{"_id": bson.NewObjectID(), "organizationId": orgA, "createdBy": adminB, "name": "cross-tenant", "createdAt": now, "updatedAt": now}},
	}
	sink := &fakeSink{}
	counts, err := Run(context.Background(), source, sink, false)
	if err == nil || !strings.Contains(err.Error(), "applications record 1") {
		t.Fatalf("expected sanitized cross-tenant location, got counts=%#v err=%v", counts, err)
	}
	if sink.migrated != 0 || sink.putCalls != 0 {
		t.Fatalf("cross-tenant reference allowed writes: migrated=%d puts=%d", sink.migrated, sink.putCalls)
	}
}

func TestRunReturnsCountsIncludingCurrentCollectionOnInsertFailure(t *testing.T) {
	now := time.Now()
	source := fakeSource{"organizations": {
		{"_id": bson.NewObjectID(), "name": "one", "email": "one@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now},
		{"_id": bson.NewObjectID(), "name": "two", "email": "two@example.invalid", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now},
	}}
	sink := &fakeSink{putError: errors.New("storage failure")}
	counts, err := Run(context.Background(), source, sink, false)
	if err == nil || !strings.Contains(err.Error(), "insert organizations record 2") {
		t.Fatalf("expected sanitized insert location, got counts=%#v err=%v", counts, err)
	}
	if got := counts["organizations"]; got.Read != 2 || got.Inserted != 1 || got.Skipped != 0 {
		t.Fatalf("partial counts missing current collection: %#v", counts)
	}
}

func TestConvertPreservesObjectIDTimesAndDeletedState(t *testing.T) {
	id := bson.NewObjectID()
	org := bson.NewObjectID()
	now := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	got, err := convert("applications", bson.M{"_id": id, "organizationId": org, "createdBy": "admin", "name": "app", "createdAt": now, "updatedAt": now, "deletedAt": bson.DateTime(now.UnixMilli())})
	if err != nil {
		t.Fatal(err)
	}
	v := got.(repository.Application)
	if v.ID != id.Hex() || v.OrganizationID != org.Hex() || !v.Deleted || !v.CreatedAt.Equal(now) ||
		v.DeletedAt == nil || !v.DeletedAt.Equal(now) {
		t.Fatalf("conversion lost source fields: %#v", v)
	}
}

func TestConvertPreservesOrganizationDeletedAtTimestamp(t *testing.T) {
	now := time.Date(2022, 3, 4, 5, 6, 7, 0, time.FixedZone("source", 9*60*60))
	deletedAt := now.Add(2 * time.Hour)
	got, err := convert("organizations", bson.M{
		"_id": bson.NewObjectID(), "name": "n", "email": "e", "secret": testBcryptHash(t),
		"createdAt": bson.DateTime(now.UnixMilli()), "updatedAt": bson.DateTime(now.UnixMilli()),
		"deleted": true, "deletedAt": bson.DateTime(deletedAt.UnixMilli()),
	})
	if err != nil {
		t.Fatal(err)
	}
	v := got.(repository.Organization)
	if !v.Deleted || v.DeletedAt == nil || !v.DeletedAt.Equal(deletedAt.UTC()) {
		t.Fatalf("organization deleted timestamp was lost: %#v", v)
	}
}

func TestConvertPreservesLegacyDeletedBooleanWithoutTimestamp(t *testing.T) {
	now := time.Now()
	got, err := convert("applications", bson.M{
		"_id": bson.NewObjectID(), "organizationId": bson.NewObjectID(),
		"createdBy": "admin", "name": "app", "createdAt": now, "updatedAt": now,
		"deleted": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.(repository.Application).Deleted {
		t.Fatal("legacy deleted boolean was lost")
	}
}

func TestConvertRejectsInvalidDeletedBoolean(t *testing.T) {
	now := time.Now()
	_, err := convert("applications", bson.M{
		"_id": bson.NewObjectID(), "organizationId": bson.NewObjectID(),
		"createdBy": "admin", "name": "app", "createdAt": now, "updatedAt": now,
		"deleted": "yes",
	})
	if err == nil {
		t.Fatal("expected invalid deleted value to fail")
	}
}

func TestConvertRejectsEmptyRequiredStrings(t *testing.T) {
	now := time.Now()
	_, err := convert("organizations", bson.M{
		"_id": bson.NewObjectID(), "name": "", "email": "e", "secret": testBcryptHash(t),
		"createdAt": now, "updatedAt": now,
	})
	if err == nil {
		t.Fatal("expected missing required organization field to fail")
	}
}

func TestConvertPreservesCommentRelationsAndArrays(t *testing.T) {
	id := bson.NewObjectID()
	app := bson.NewObjectID()
	reply := bson.NewObjectID()
	now := time.Now().UTC()
	got, err := convert("comments", bson.M{
		"_id": id, "applicationId": app, "ownerId": "customer", "content": "body",
		"replies": bson.A{reply}, "flags": bson.A{"a", "b"}, "upVotes": bson.A{"c"},
		"downVotes": bson.A{"d"}, "createdAt": now, "updatedAt": now,
	})
	if err != nil {
		t.Fatal(err)
	}
	v := got.(repository.Comment)
	if v.ID != id.Hex() || v.ApplicationID != app.Hex() || len(v.Replies) != 1 || v.Replies[0] != reply.Hex() ||
		len(v.Flags) != 2 || v.Flags[1] != "b" || v.UpVotes[0] != "c" || v.DownVotes[0] != "d" {
		t.Fatalf("conversion lost comment fields: %#v", v)
	}
}

func TestRunDryRunValidatesAndCountsWithoutSink(t *testing.T) {
	s := fakeSource{"organizations": {{"_id": bson.NewObjectID(), "name": "n", "email": "e", "secret": testBcryptHash(t), "createdAt": time.Now(), "updatedAt": time.Now()}}}
	sink := &fakeSink{}
	report, err := Run(context.Background(), s, sink, true)
	if err != nil {
		t.Fatal(err)
	}
	if sink.migrated != 0 || len(sink.rows) != 0 {
		t.Fatal("dry run touched sink")
	}
	if report["organizations"].Read != 1 || report["organizations"].Inserted != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestRunDryRunRejectsValuesThatExceedStorageLimits(t *testing.T) {
	source := fakeSource{"organizations": {{
		"_id": bson.NewObjectID(), "name": strings.Repeat("n", 256), "email": "valid@example.invalid",
		"secret": testBcryptHash(t), "createdAt": time.Now(), "updatedAt": time.Now(),
	}}}
	if _, err := Run(context.Background(), source, &fakeSink{}, true); err == nil {
		t.Fatal("expected dry-run to reject a name that MySQL cannot store")
	}
}

func TestRunRejectsMalformedDocumentInsteadOfSkipping(t *testing.T) {
	_, err := Run(context.Background(), fakeSource{"admins": {{"_id": bson.NewObjectID()}}}, &fakeSink{}, true)
	if err == nil {
		t.Fatal("expected malformed source document to fail")
	}
}

func TestConvertRejectsMalformedDeletedState(t *testing.T) {
	now := time.Now()
	_, err := convert("organizations", bson.M{
		"_id": bson.NewObjectID(), "name": "n", "email": "e", "secret": testBcryptHash(t),
		"createdAt": now, "updatedAt": now, "deletedAt": "yes",
	})
	if err == nil {
		t.Fatal("expected malformed deletedAt to fail")
	}
}

func TestRunInsertOnlyCountsPreexistingIDsAsSkipped(t *testing.T) {
	id := bson.NewObjectID()
	now := time.Now()
	source := fakeSource{"organizations": {{"_id": id, "name": "old", "email": "e", "secret": testBcryptHash(t), "createdAt": now, "updatedAt": now}}}
	sink := &fakeSink{rows: map[string]map[string]any{"organizations": {id.Hex(): repository.Organization{ID: id.Hex(), Name: "new"}}}}
	report, err := Run(context.Background(), source, sink, false)
	if err != nil {
		t.Fatal(err)
	}
	c := report["organizations"]
	if c.Read != 1 || c.Inserted != 0 || c.Skipped != 1 {
		t.Fatalf("unexpected counts: %#v", c)
	}
	if sink.migrated != 1 {
		t.Fatal("expected schema migration before import")
	}
}

func TestConvertRejectsPlaintextOrganizationSecret(t *testing.T) {
	_, err := convert("organizations", bson.M{
		"_id": bson.NewObjectID(), "name": "n", "email": "e", "secret": "plaintext",
		"createdAt": time.Now(), "updatedAt": time.Now(),
	})
	if err == nil {
		t.Fatal("expected plaintext organization secret to fail")
	}
}

func TestConvertRejectsMalformedAdminPasswordHash(t *testing.T) {
	_, err := convert("admins", bson.M{
		"_id": bson.NewObjectID(), "organizationId": bson.NewObjectID(),
		"fullname": "name", "email": "e", "password": "not-a-bcrypt-hash",
		"createdAt": time.Now(), "updatedAt": time.Now(),
	})
	if err == nil {
		t.Fatal("expected malformed admin password hash to fail")
	}
}
