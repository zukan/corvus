package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func mockedMySQL(t *testing.T) (*MySQL, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &MySQL{db: db}, mock
}
func q(s string) string { return regexp.QuoteMeta(s) }

func TestMySQLListCommentsAppliesTenantFiltersAndStablePaging(t *testing.T) {
	r, m := mockedMySQL(t)
	owner, ref := "owner-1", "ref-1"
	flagged := true
	m.ExpectQuery(q("SELECT COUNT(*) FROM comments WHERE application_id=? AND deleted=FALSE AND ref_id=? AND owner_id=? AND (JSON_LENGTH(flags)>0)=?")).WithArgs("app-1", ref, owner, true).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	m.ExpectQuery(q(commentSelect+" WHERE application_id=? AND deleted=FALSE AND ref_id=? AND owner_id=? AND (JSON_LENGTH(flags)>0)=? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")).WithArgs("app-1", ref, owner, true, 1, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "application_id", "owner_id", "content", "ref_id", "origin", "replies", "flags", "up_votes", "down_votes", "created_at", "updated_at", "deleted", "deleted_at"}).AddRow("c-2", "app-1", owner, "body", ref, nil, "[]", `["f"]`, `["u"]`, `[]`, time.Now(), time.Now(), false, nil))
	got, total, err := r.ListComments(context.Background(), Filter{ApplicationID: "app-1", RefID: &ref, OwnerID: &owner, Flagged: &flagged, Page: 2, Limit: 1, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 1 || got[0].ID != "c-2" || len(got[0].Flags) != 1 || len(got[0].UpVotes) != 1 {
		t.Fatalf("result = %#v total=%d", got, total)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLCreateOrganizationRollsBackAdminFailure(t *testing.T) {
	r, m := mockedMySQL(t)
	m.ExpectBegin()
	m.ExpectExec(`INSERT INTO organizations`).WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectExec(`INSERT INTO admins`).WillReturnError(errors.New("admin insert failed"))
	m.ExpectRollback()
	err := r.CreateOrganization(context.Background(), Organization{ID: "org"}, Admin{ID: "admin", OrganizationID: "org"})
	if err == nil {
		t.Fatal("expected insert failure")
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestMySQLUpdateCommentRejectsWrongTenantAndRollsBackWriteFailure(t *testing.T) {
	t.Run("tenant scoped lookup", func(t *testing.T) {
		r, m := mockedMySQL(t)
		m.ExpectBegin()
		m.ExpectQuery(q(commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE FOR UPDATE")).WithArgs("other-app", "c").WillReturnError(sql.ErrNoRows)
		m.ExpectRollback()
		if _, err := r.UpdateComment(context.Background(), "other-app", "c", "owner", "new"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("write failure", func(t *testing.T) {
		r, m := mockedMySQL(t)
		now := time.Now()
		cols := []string{"id", "application_id", "owner_id", "content", "ref_id", "origin", "replies", "flags", "up_votes", "down_votes", "created_at", "updated_at", "deleted", "deleted_at"}
		m.ExpectBegin()
		m.ExpectQuery(q(commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE FOR UPDATE")).WithArgs("app", "c").WillReturnRows(sqlmock.NewRows(cols).AddRow("c", "app", "owner", "old", nil, nil, "[]", "[]", "[]", "[]", now, now, false, nil))
		m.ExpectExec(`UPDATE comments SET content=\?,updated_at=\? WHERE application_id=\? AND id=\?`).WithArgs("new", sqlmock.AnyArg(), "app", "c").WillReturnError(errors.New("write failed"))
		m.ExpectRollback()
		if _, err := r.UpdateComment(context.Background(), "app", "c", "owner", "new"); err == nil {
			t.Fatal("expected update error")
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMySQLDeleteCommentRollsBackBothSoftDeletesAtomically(t *testing.T) {
	r, m := mockedMySQL(t)
	now := time.Now()
	cols := []string{"id", "application_id", "owner_id", "content", "ref_id", "origin", "replies", "flags", "up_votes", "down_votes", "created_at", "updated_at", "deleted", "deleted_at"}
	m.ExpectBegin()
	m.ExpectQuery(q(commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE FOR UPDATE")).WithArgs("app", "c").WillReturnRows(sqlmock.NewRows(cols).AddRow("c", "app", "owner", "body", nil, nil, "[]", "[]", "[]", "[]", now, now, false, nil))
	m.ExpectExec(`UPDATE comments SET deleted=TRUE`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "app", "c").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec(`UPDATE comment_replies SET deleted=TRUE`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "c").WillReturnError(errors.New("reply update failed"))
	m.ExpectRollback()
	if _, err := r.DeleteComment(context.Background(), "app", "c", "owner"); err == nil {
		t.Fatal("expected reply update error")
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLPutImportedDuplicateIDIsIdempotentButUniqueConflictIsError(t *testing.T) {
	r, m := mockedMySQL(t)
	dup := &mysql.MySQLError{Number: 1062, Message: "duplicate"}
	m.ExpectExec(`INSERT INTO organizations`).WillReturnError(dup)
	m.ExpectQuery(`SELECT COUNT\(\*\) FROM organizations WHERE id=\?`).WithArgs("existing").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	inserted, err := r.PutImported(context.Background(), "organizations", Organization{ID: "existing"})
	if err != nil || inserted {
		t.Fatalf("idempotent duplicate = %v, %v", inserted, err)
	}
	m.ExpectExec(`INSERT INTO organizations`).WillReturnError(dup)
	m.ExpectQuery(`SELECT COUNT\(\*\) FROM organizations WHERE id=\?`).WithArgs("new-id").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	inserted, err = r.PutImported(context.Background(), "organizations", Organization{ID: "new-id"})
	if inserted || !errors.Is(err, ErrConflict) {
		t.Fatalf("unique conflict = %v, %v", inserted, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLMigrateRollsBackFailedVersionedMigration(t *testing.T) {
	r, m := mockedMySQL(t)
	m.ExpectQuery(`SELECT GET_LOCK\(\?, \?\)`).WithArgs("corvus_schema_migrations", 10).WillReturnRows(sqlmock.NewRows([]string{"lock"}).AddRow(1))
	m.ExpectExec(`CREATE TABLE IF NOT EXISTS schema_migrations`).WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_migrations WHERE version=\?`).WithArgs("001_init").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	m.ExpectBegin()
	m.ExpectExec(`CREATE TABLE IF NOT EXISTS organizations`).WillReturnError(errors.New("ddl failure"))
	m.ExpectRollback()
	m.ExpectExec(`SELECT RELEASE_LOCK\(\?\)`).WithArgs("corvus_schema_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := r.Migrate(context.Background()); err == nil {
		t.Fatal("expected migration failure")
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLPutImportedPreservesDeletedStateAndReplyArrays(t *testing.T) {
	r, m := mockedMySQL(t)
	deleted := true
	m.ExpectExec(`INSERT INTO applications`).WithArgs("app", "org", "creator", "name", sqlmock.AnyArg(), sqlmock.AnyArg(), true, nil).WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err := r.PutImported(context.Background(), "applications", Application{ID: "app", OrganizationID: "org", CreatedBy: "creator", Name: "name", Deleted: deleted})
	if err != nil || !inserted {
		t.Fatalf("deleted application import = %v, %v", inserted, err)
	}
	m.ExpectExec(`INSERT INTO comment_replies`).WithArgs("reply", "comment", "owner", "text", []byte(`["flag"]`), []byte(`["up"]`), []byte(`["down"]`), sqlmock.AnyArg(), sqlmock.AnyArg(), true, nil).WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err = r.PutImported(context.Background(), "replies", Reply{ID: "reply", CommentID: "comment", OwnerID: "owner", Content: "text", Flags: []string{"flag"}, UpVotes: []string{"up"}, DownVotes: []string{"down"}, Deleted: true})
	if err != nil || !inserted {
		t.Fatalf("reply import = %v, %v", inserted, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLDSNEnablesTimestampParsing(t *testing.T) {
	cfg, err := parseMySQLConfig("user:pass@tcp(localhost:3306)/test")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime {
		t.Fatal("ParseTime must be enabled")
	}
	if cfg.Loc != time.UTC {
		t.Fatalf("MySQL timestamp location = %v", cfg.Loc)
	}
}

func TestJSONCollectionsSerializeEmptySlicesAsArrays(t *testing.T) {
	if got := string(jsonValue(nil)); got != "[]" {
		t.Fatalf("nil collection JSON = %s", got)
	}
	if got := string(jsonValue([]string{})); got != "[]" {
		t.Fatalf("empty collection JSON = %s", got)
	}
}

func TestMySQLCreateApplicationRejectsDuplicateTenantName(t *testing.T) {
	r, m := mockedMySQL(t)
	m.ExpectExec(`INSERT INTO applications`).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate tenant name"})
	err := r.CreateApplication(context.Background(), Application{ID: "new", OrganizationID: "org", Name: "same"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate application name error = %v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDBErrMapsMissingForeignKeyToNotFound(t *testing.T) {
	err := dbErr(&mysql.MySQLError{Number: 1452, Message: "foreign key constraint fails"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign key error = %v", err)
	}
}

func TestRecordLengthValidationRejectsOverflow(t *testing.T) {
	if err := validateID("id", strings.Repeat("x", 192)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("id validation = %v", err)
	}
	if err := validateVarchar("name", strings.Repeat("x", 256), 255); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name validation = %v", err)
	}
	if err := validateVarchar("name", strings.Repeat("界", 255), 255); err != nil {
		t.Fatalf("unicode varchar validation = %v", err)
	}
	if err := validateComment(Comment{ID: "c", ApplicationID: "app", Content: strings.Repeat("x", maxMediumTextBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized content validation = %v", err)
	}
	if err := validateComment(Comment{ID: "c", ApplicationID: "app", Content: strings.Repeat("x", 1<<20)}); err != nil {
		t.Fatalf("1MiB content should fit MEDIUMTEXT: %v", err)
	}
}

func TestMySQLImportedDeletedAtPersistsUTCAtMillisecondPrecision(t *testing.T) {
	r, m := mockedMySQL(t)
	source := time.Date(2021, 2, 3, 4, 5, 6, 987654321, time.FixedZone("source", -7*60*60))
	expected := source.UTC().Truncate(time.Millisecond)
	m.ExpectExec(`INSERT INTO organizations`).WithArgs("deleted-org", "Org", "org@example.com", "secret", sqlmock.AnyArg(), sqlmock.AnyArg(), true, expected).WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err := r.PutImported(context.Background(), "organizations", Organization{ID: "deleted-org", Name: "Org", Email: "org@example.com", Secret: "secret", Deleted: true, DeletedAt: &source})
	if err != nil || !inserted {
		t.Fatalf("organization import = %v,%v", inserted, err)
	}
	m.ExpectExec(`INSERT INTO applications`).WithArgs("deleted-app", "deleted-org", "creator", "Archived", sqlmock.AnyArg(), sqlmock.AnyArg(), true, expected).WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err = r.PutImported(context.Background(), "applications", Application{ID: "deleted-app", OrganizationID: "deleted-org", CreatedBy: "creator", Name: "Archived", Deleted: true, DeletedAt: &source})
	if err != nil || !inserted {
		t.Fatalf("application import = %v,%v", inserted, err)
	}
	cols := []string{"id", "application_id", "owner_id", "content", "ref_id", "origin", "replies", "flags", "up_votes", "down_votes", "created_at", "updated_at", "deleted", "deleted_at"}
	m.ExpectQuery(q(commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE")).WithArgs("app", "c").WillReturnRows(sqlmock.NewRows(cols).AddRow("c", "app", "owner", "body", nil, nil, "[]", "[]", "[]", "[]", expected, expected, true, expected))
	got, err := r.GetComment(context.Background(), "app", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == nil || !got.DeletedAt.Equal(expected) {
		t.Fatalf("scanned deleted_at = %v want %v", got.DeletedAt, expected)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateImportedUsesStorageLimitsAndKindTypes(t *testing.T) {
	if err := ValidateImported("applications", Application{ID: "a", OrganizationID: "o", Name: strings.Repeat("x", 256)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized application import validation = %v", err)
	}
	if err := ValidateImported("comments", Organization{ID: "wrong-type"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong import type validation = %v", err)
	}
	if err := ValidateImported("unknown", Application{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown kind validation = %v", err)
	}
}
