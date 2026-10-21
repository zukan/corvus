package migration

import (
	"context"
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
	migrated int
	rows     map[string]map[string]any
}

func (f *fakeSink) Migrate(context.Context) error { f.migrated++; return nil }
func (f *fakeSink) PutImported(_ context.Context, kind string, value any) (bool, error) {
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
