//go:build integration

package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zukan/corvus/internal/repository"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"
)

type dryRunSpy struct{ migrations, writes int }

func (s *dryRunSpy) Migrate(context.Context) error { s.migrations++; return nil }
func (s *dryRunSpy) PutImported(context.Context, string, any) (bool, error) {
	s.writes++
	return true, nil
}

type trackingSink struct {
	target     Sink
	migrations int
	writes     int
}

func TestTargetApplicationBindingIntegration(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_TEST_DSN to a disposable MySQL service")
	}
	for _, tc := range []struct {
		name, organization, creator string
		conflict                    bool
	}{
		{"different organization", "other-org", "other-admin", true},
		{"different creator", "source-org", "other-admin", true},
		{"identical bindings", "source-org", "source-admin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cfg, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatal("parse disposable MySQL test DSN failed")
			}
			cfg.DBName = ""
			adminDB, err := sql.Open("mysql", cfg.FormatDSN())
			if err != nil {
				t.Fatal("open disposable MySQL setup connection failed")
			}
			defer adminDB.Close()
			database := "corvus_binding_" + bson.NewObjectID().Hex()
			if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
				t.Fatal("create isolated MySQL test database failed")
			}
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cleanupCancel()
				if _, err := adminDB.ExecContext(cleanupCtx, "DROP DATABASE `"+database+"`"); err != nil {
					t.Error("remove isolated MySQL test database failed")
				}
			}()
			cfg.DBName = database
			target, err := repository.Open(ctx, cfg.FormatDSN())
			if err != nil {
				t.Fatal("open isolated MySQL test database failed")
			}
			defer target.Close()
			if err := target.Migrate(ctx); err != nil {
				t.Fatal("migrate isolated MySQL test database failed")
			}
			source := validImportSource(t)
			for _, collection := range []string{"organizations", "admins", "applications"} {
				value, err := convert(collection, source[collection][0])
				if err != nil {
					t.Fatal("convert synthetic target fixture failed")
				}
				switch v := value.(type) {
				case repository.Organization:
					v.ID, v.Email = tc.organization, "target-org@example.invalid"
					value = v
				case repository.Admin:
					v.ID, v.OrganizationID = tc.creator, tc.organization
					v.Email = "target-admin@example.invalid"
					value = v
				case repository.Application:
					v.OrganizationID, v.CreatedBy, v.Name = tc.organization, tc.creator, "Existing App"
					value = v
				}
				if inserted, err := target.PutImported(ctx, collection, value); err != nil || !inserted {
					t.Fatal("seed synthetic target fixture failed")
				}
			}
			report, runErr := Run(ctx, source, target, false)
			if tc.conflict {
				if runErr == nil || runErr.Error() != "insert applications record 1 failed" {
					t.Errorf("expected sanitized application conflict, got %v", runErr)
				}
			} else {
				if runErr != nil || report["applications"].Skipped != 1 || report["comments"].Inserted != 1 {
					t.Fatalf("identical bindings did not skip and import children: %v", runErr)
				}
				report, runErr = Run(ctx, source, target, false)
				if runErr != nil {
					t.Fatalf("repeat import failed: %v", runErr)
				}
				assertCounts(t, report, 1, 0, 1)
			}
			var organization, creator, name string
			if err := adminDB.QueryRowContext(ctx, "SELECT organization_id,created_by,name FROM `"+database+"`.applications WHERE id=?", "shared-app").Scan(&organization, &creator, &name); err != nil {
				t.Fatal("inspect target application failed")
			}
			if organization != tc.organization || creator != tc.creator || name != "Existing App" {
				t.Error("target application was overwritten or moved")
			}
			var count int
			if err := adminDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+database+"`.comments WHERE id=?", "source-comment").Scan(&count); err != nil {
				t.Fatal("inspect target comments failed")
			}
			if tc.conflict && count != 0 {
				t.Error("conflicting application allowed child comment insertion")
			}
		})
	}
}

func (s *trackingSink) Migrate(ctx context.Context) error {
	s.migrations++
	return s.target.Migrate(ctx)
}
func (s *trackingSink) PutImported(ctx context.Context, collection string, value any) (bool, error) {
	s.writes++
	return s.target.PutImported(ctx, collection, value)
}

func TestOrphanMongoInputDoesNotTouchLiveMySQLIntegration(t *testing.T) {
	mongoURI := os.Getenv("MONGO_TEST_URI")
	mysqlDSN := os.Getenv("MYSQL_TEST_DSN")
	if mongoURI == "" || mysqlDSN == "" {
		t.Skip("set MONGO_TEST_URI and MYSQL_TEST_DSN to isolated disposable services")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dbName := "corvus_orphan_" + strings.ToLower(bson.NewObjectID().Hex())
	source, err := OpenMongo(ctx, mongoURI, dbName)
	if err != nil {
		t.Fatal("open Mongo test service failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = source.database.Drop(cleanupCtx)
		_ = source.Close(cleanupCtx)
	}()

	targetConfig, err := mysql.ParseDSN(mysqlDSN)
	if err != nil {
		t.Fatal("parse disposable MySQL test DSN failed")
	}
	targetDatabase := "corvus_orphan_" + strings.ToLower(bson.NewObjectID().Hex())
	targetConfig.DBName = ""
	adminDB, err := sql.Open("mysql", targetConfig.FormatDSN())
	if err != nil {
		t.Fatal("open disposable MySQL setup connection failed")
	}
	defer adminDB.Close()
	if err = adminDB.PingContext(ctx); err != nil {
		t.Fatal("connect to disposable MySQL test service failed")
	}
	if _, err = adminDB.ExecContext(ctx, "CREATE DATABASE `"+targetDatabase+"`"); err != nil {
		t.Fatal("create isolated MySQL test database failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+targetDatabase+"`")
	}()
	targetConfig.DBName = targetDatabase
	target, err := repository.Open(ctx, targetConfig.FormatDSN())
	if err != nil {
		t.Fatal("open isolated MySQL test database failed")
	}
	defer target.Close()
	spy := &trackingSink{target: target}

	now := time.Now().UTC()
	orgID := bson.NewObjectID()
	if _, err := source.database.Collection("organizations").InsertOne(ctx, bson.M{
		"_id": orgID, "name": "Synthetic Org", "email": "org-" + orgID.Hex() + "@example.invalid",
		"secret": integrationHash(t, "synthetic-secret"), "createdAt": now, "updatedAt": now,
	}); err != nil {
		t.Fatal("insert synthetic organization failed")
	}
	if _, err := source.database.Collection("comments").InsertOne(ctx, bson.M{
		"_id": bson.NewObjectID(), "applicationId": bson.NewObjectID(), "ownerId": "synthetic-owner",
		"content": "orphan comment", "createdAt": now, "updatedAt": now,
	}); err != nil {
		t.Fatal("insert synthetic orphan comment failed")
	}

	for _, dryRun := range []bool{true, false} {
		counts, runErr := Run(ctx, source, spy, dryRun)
		if runErr == nil || !strings.Contains(runErr.Error(), "comments record 1") {
			t.Fatalf("dryRun=%t: expected sanitized orphan location, got counts=%#v err=%v", dryRun, counts, runErr)
		}
		if counts["organizations"].Read != 1 || counts["comments"].Read != 1 {
			t.Fatalf("dryRun=%t: preflight counts incomplete: %#v", dryRun, counts)
		}
		var tableCount int
		if err = adminDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=?", targetDatabase).Scan(&tableCount); err != nil {
			t.Fatal("inspect isolated MySQL schema failed")
		}
		if spy.migrations != 0 || spy.writes != 0 || tableCount != 0 {
			t.Fatalf("dryRun=%t: orphan input touched MySQL: migrations=%d writes=%d tables=%d", dryRun, spy.migrations, spy.writes, tableCount)
		}
	}
}

func TestMongoToMySQLIntegration(t *testing.T) {
	mongoURI := os.Getenv("MONGO_TEST_URI")
	mysqlDSN := os.Getenv("MYSQL_TEST_DSN")
	if mongoURI == "" || mysqlDSN == "" {
		t.Skip("set MONGO_TEST_URI and MYSQL_TEST_DSN to isolated disposable services")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dbName := "corvus_integration_" + strings.ToLower(bson.NewObjectID().Hex())
	source, err := OpenMongo(ctx, mongoURI, dbName)
	if err != nil {
		t.Fatal("open Mongo test service failed")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = source.database.Drop(cleanupCtx)
		_ = source.Close(cleanupCtx)
	}()

	now := time.Date(2023, 7, 8, 9, 10, 11, 123000000, time.UTC)
	deletedAt := now.Add(time.Hour)
	orgID, adminID := bson.NewObjectID(), bson.NewObjectID()
	appID, commentID, replyID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	orgSecret := integrationHash(t, "org-secret")
	adminPassword := integrationHash(t, "admin-password")
	fixtures := map[string]bson.M{
		"organizations": {
			"_id": orgID, "name": "Synthetic Org", "email": "org-" + orgID.Hex() + "@example.invalid",
			"secret": orgSecret, "createdAt": now, "updatedAt": now, "deletedAt": deletedAt,
		},
		"admins": {
			"_id": adminID, "organizationId": orgID, "fullname": "Synthetic Admin",
			"email": "admin-" + adminID.Hex() + "@example.invalid", "password": adminPassword,
			"createdAt": now, "updatedAt": now,
		},
		"applications": {
			"_id": appID, "organizationId": orgID, "createdBy": adminID, "name": "Synthetic App",
			"createdAt": now, "updatedAt": now, "deleted": true, "deletedAt": deletedAt,
		},
		"comments": {
			"_id": commentID, "applicationId": appID, "ownerId": "synthetic-user", "content": "synthetic comment",
			"refId": "ref-1", "origin": "integration", "replies": bson.A{replyID},
			"flags": bson.A{"flagger-a", "flagger-b"}, "upVotes": bson.A{"voter-up"},
			"downVotes": bson.A{"voter-down"}, "createdAt": now, "updatedAt": now,
		},
		"replies": {
			"_id": replyID, "commentId": commentID, "ownerId": "synthetic-replier", "content": "synthetic reply",
			"flags": bson.A{"reply-flag"}, "upVotes": bson.A{"reply-up"}, "downVotes": bson.A{"reply-down"},
			"createdAt": now, "updatedAt": now,
		},
	}
	for collection, fixture := range fixtures {
		if _, err := source.database.Collection(collection).InsertOne(ctx, fixture); err != nil {
			t.Fatalf("insert Mongo fixture into %s failed", collection)
		}
	}

	spy := &dryRunSpy{}
	dryReport, err := Run(ctx, source, spy, true)
	if err != nil {
		t.Fatal("dry-run validation failed")
	}
	if spy.migrations != 0 || spy.writes != 0 {
		t.Fatalf("dry-run called MySQL sink: migrations=%d writes=%d", spy.migrations, spy.writes)
	}
	assertCounts(t, dryReport, 1, 0, 0)

	repo, err := repository.Open(ctx, mysqlDSN)
	if err != nil {
		t.Fatal("open disposable MySQL test service failed")
	}
	defer repo.Close()
	first, err := Run(ctx, source, repo, false)
	if err != nil {
		t.Fatal("first import failed")
	}
	assertCounts(t, first, 1, 1, 0)

	// Change the source row between passes; insert-only behavior must preserve the
	// first imported value for the same ID.
	if _, err = source.database.Collection("organizations").UpdateByID(ctx, orgID, bson.M{"$set": bson.M{"name": "Changed Source"}}); err != nil {
		t.Fatal("update synthetic Mongo fixture failed")
	}
	second, err := Run(ctx, source, repo, false)
	if err != nil {
		t.Fatal("repeat import failed")
	}
	assertCounts(t, second, 1, 0, 1)

	cfg, err := mysql.ParseDSN(mysqlDSN)
	if err != nil {
		t.Fatal("parse disposable MySQL test DSN failed")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open MySQL verification connection failed")
	}
	defer db.Close()
	verifyIntegrationRows(t, ctx, db, orgID, adminID, appID, commentID, replyID, orgSecret, adminPassword, now, deletedAt)
}

func integrationHash(t *testing.T, value string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.MinCost)
	if err != nil {
		t.Fatal("create synthetic bcrypt fixture failed")
	}
	return string(hash)
}

func assertCounts(t *testing.T, report map[string]Counts, read, inserted, skipped int) {
	t.Helper()
	for _, collection := range collections {
		count := report[collection]
		if count.Read != read || count.Inserted != inserted || count.Skipped != skipped {
			t.Fatalf("%s counts = %#v, want read=%d inserted=%d skipped=%d", collection, count, read, inserted, skipped)
		}
	}
}

func verifyIntegrationRows(t *testing.T, ctx context.Context, db *sql.DB, orgID, adminID, appID, commentID, replyID bson.ObjectID, orgSecret, adminPassword string, now, deletedAt time.Time) {
	t.Helper()
	var id, name, email, secret string
	var created, updated time.Time
	var orgDeletedAt sql.NullTime
	var deleted bool
	if err := db.QueryRowContext(ctx, "SELECT id,name,email,secret,created_at,updated_at,deleted,deleted_at FROM organizations WHERE id=?", orgID.Hex()).Scan(&id, &name, &email, &secret, &created, &updated, &deleted, &orgDeletedAt); err != nil {
		t.Fatal("read imported organization failed")
	}
	if id != orgID.Hex() || name != "Synthetic Org" || secret != orgSecret || !created.Equal(now) || !updated.Equal(now) || !deleted || !orgDeletedAt.Valid || !orgDeletedAt.Time.Equal(deletedAt) {
		t.Fatalf("organization preservation mismatch: id=%q name=%q timestamps=%v/%v deleted=%v", id, name, created, updated, deleted)
	}
	var password string
	if err := db.QueryRowContext(ctx, "SELECT password FROM admins WHERE id=?", adminID.Hex()).Scan(&password); err != nil || password != adminPassword {
		t.Fatal("admin password hash was not preserved")
	}
	var appDeletedAt sql.NullTime
	if err := db.QueryRowContext(ctx, "SELECT deleted,deleted_at FROM applications WHERE id=?", appID.Hex()).Scan(&deleted, &appDeletedAt); err != nil || !deleted || !appDeletedAt.Valid || !appDeletedAt.Time.Equal(deletedAt) {
		t.Fatal("application deleted state and timestamp were not preserved")
	}
	var applicationID, owner, content, refID, origin string
	var repliesRaw, flagsRaw, upVotesRaw, downVotesRaw []byte
	if err := db.QueryRowContext(ctx, "SELECT application_id,owner_id,content,ref_id,origin,replies,flags,up_votes,down_votes FROM comments WHERE id=?", commentID.Hex()).Scan(&applicationID, &owner, &content, &refID, &origin, &repliesRaw, &flagsRaw, &upVotesRaw, &downVotesRaw); err != nil {
		t.Fatal("read imported comment failed")
	}
	var replies, flags, upVotes, downVotes []string
	for i, raw := range [][]byte{repliesRaw, flagsRaw, upVotesRaw, downVotesRaw} {
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal("decode imported comment array failed")
		}
		switch i {
		case 0:
			replies = values
		case 1:
			flags = values
		case 2:
			upVotes = values
		case 3:
			downVotes = values
		}
	}
	if applicationID != appID.Hex() || owner != "synthetic-user" || content != "synthetic comment" || refID != "ref-1" || origin != "integration" ||
		!reflect.DeepEqual(replies, []string{replyID.Hex()}) || !reflect.DeepEqual(flags, []string{"flagger-a", "flagger-b"}) ||
		!reflect.DeepEqual(upVotes, []string{"voter-up"}) || !reflect.DeepEqual(downVotes, []string{"voter-down"}) {
		t.Fatal("comment relations or arrays were not preserved")
	}
	var commentRef string
	if err := db.QueryRowContext(ctx, "SELECT comment_id FROM comment_replies WHERE id=?", replyID.Hex()).Scan(&commentRef); err != nil || commentRef != commentID.Hex() {
		t.Fatal("reply relation was not preserved")
	}
}
