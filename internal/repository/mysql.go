package repository

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type MySQL struct{ db *sql.DB }

func parseMySQLConfig(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	// Keep connections bounded even for callers outside the HTTP server. Request
	// contexts may impose a shorter deadline; positive DSN overrides are honored.
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 10 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	return cfg, nil
}
func Open(ctx context.Context, dsn string) (*MySQL, error) {
	cfg, err := parseMySQLConfig(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &MySQL{db: db}, nil
}
func (m *MySQL) Close() error                   { return m.db.Close() }
func (m *MySQL) Ping(ctx context.Context) error { return m.db.PingContext(ctx) }
func latestSchemaVersion() (string, error) {
	files, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("no migrations are defined")
	}
	return strings.TrimSuffix(files[len(files)-1].Name(), ".sql"), nil
}
func (m *MySQL) Ready(ctx context.Context) error {
	if err := m.db.PingContext(ctx); err != nil {
		return err
	}
	requiredVersion, err := latestSchemaVersion()
	if err != nil {
		return fmt.Errorf("required schema version is unavailable: %w", err)
	}
	var appliedVersion string
	if err := m.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE version=?", requiredVersion).Scan(&appliedVersion); err != nil {
		return fmt.Errorf("required schema migration %s is unavailable: %w", requiredVersion, err)
	}
	for _, query := range []string{
		"SELECT id,name,email,secret,created_at,updated_at,deleted,deleted_at FROM organizations LIMIT 0",
		"SELECT id,organization_id,fullname,email,password,created_at,updated_at FROM admins LIMIT 0",
		"SELECT id,organization_id,created_by,name,created_at,updated_at,deleted,deleted_at FROM applications LIMIT 0",
		"SELECT id,application_id,owner_id,content,ref_id,origin,replies,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at FROM comments LIMIT 0",
		"SELECT id,comment_id,owner_id,content,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at FROM comment_replies LIMIT 0",
	} {
		rows, err := m.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("required schema is unavailable: %w", err)
		}
		if err = rows.Close(); err != nil {
			return fmt.Errorf("required schema check failed: %w", err)
		}
	}
	return nil
}
func (m *MySQL) Migrate(ctx context.Context) error {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", "corvus_schema_migrations", 10).Scan(&locked); err != nil {
		return err
	}
	if locked != 1 {
		return errors.New("could not acquire migration lock")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(releaseCtx, "SELECT RELEASE_LOCK(?)", "corvus_schema_migrations")
	}()
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(255) PRIMARY KEY, applied_at DATETIME(3) NOT NULL)`); err != nil {
		return err
	}
	files, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		version := strings.TrimSuffix(f.Name(), ".sql")
		var n int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=?", version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		data, e := migrationFiles.ReadFile("migrations/" + f.Name())
		if e != nil {
			return e
		}
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		for _, stmt := range strings.Split(string(data), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, e = tx.ExecContext(ctx, stmt); e != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s: %w", version, e)
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)", version, time.Now()); e != nil {
			_ = tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}
func dbErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		if me.Number == 1062 {
			return ErrConflict
		}
		if me.Number == 1452 || me.Number == 1216 {
			return ErrNotFound
		}
	}
	return err
}
func stamp(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC().Truncate(time.Millisecond)
	}
	return t.UTC().Truncate(time.Millisecond)
}
func jsonValue(v []string) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, _ := json.Marshal(v)
	return b
}
func decodeStrings(b []byte) []string {
	var v []string
	_ = json.Unmarshal(b, &v)
	if v == nil {
		return []string{}
	}
	return v
}
func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
func scanTime(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time.UTC().Truncate(time.Millisecond)
	return &t
}
func scanPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func (m *MySQL) CreateOrganization(ctx context.Context, o Organization, a Admin) error {
	if err := validateOrganization(o); err != nil {
		return err
	}
	if err := validateAdmin(a); err != nil {
		return err
	}
	if a.OrganizationID != o.ID {
		return ErrConflict
	}
	o.CreatedAt = stamp(o.CreatedAt)
	o.UpdatedAt = stamp(o.UpdatedAt)
	a.CreatedAt = stamp(a.CreatedAt)
	a.UpdatedAt = stamp(a.UpdatedAt)
	tx, e := m.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO organizations(id,name,email,secret,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?)", o.ID, o.Name, o.Email, o.Secret, o.CreatedAt, o.UpdatedAt, o.Deleted, nullableTime(o.DeletedAt)); e != nil {
		tx.Rollback()
		return dbErr(e)
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO admins(id,organization_id,fullname,email,password,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", a.ID, a.OrganizationID, a.Fullname, a.Email, a.Password, a.CreatedAt, a.UpdatedAt); e != nil {
		tx.Rollback()
		return dbErr(e)
	}
	return tx.Commit()
}
func (m *MySQL) GetOrganization(ctx context.Context, id string) (Organization, error) {
	var o Organization
	var deletedAt sql.NullTime
	e := m.db.QueryRowContext(ctx, "SELECT id,name,email,secret,created_at,updated_at,deleted,deleted_at FROM organizations WHERE id=? AND deleted=FALSE", id).Scan(&o.ID, &o.Name, &o.Email, &o.Secret, &o.CreatedAt, &o.UpdatedAt, &o.Deleted, &deletedAt)
	o.DeletedAt = scanTime(deletedAt)
	return o, dbErr(e)
}
func (m *MySQL) FindAdmin(ctx context.Context, org, email string) (Admin, error) {
	var a Admin
	e := m.db.QueryRowContext(ctx, "SELECT id,organization_id,fullname,email,password,created_at,updated_at FROM admins WHERE organization_id=? AND email=?", org, email).Scan(&a.ID, &a.OrganizationID, &a.Fullname, &a.Email, &a.Password, &a.CreatedAt, &a.UpdatedAt)
	return a, dbErr(e)
}
func (m *MySQL) GetAdmin(ctx context.Context, id string) (Admin, error) {
	var a Admin
	e := m.db.QueryRowContext(ctx, "SELECT id,organization_id,fullname,email,password,created_at,updated_at FROM admins WHERE id=?", id).Scan(&a.ID, &a.OrganizationID, &a.Fullname, &a.Email, &a.Password, &a.CreatedAt, &a.UpdatedAt)
	return a, dbErr(e)
}
func (m *MySQL) CreateApplication(ctx context.Context, a Application) error {
	if err := validateApplication(a); err != nil {
		return err
	}
	a.CreatedAt = stamp(a.CreatedAt)
	a.UpdatedAt = stamp(a.UpdatedAt)
	_, e := m.db.ExecContext(ctx, "INSERT INTO applications(id,organization_id,created_by,name,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?)", a.ID, a.OrganizationID, a.CreatedBy, a.Name, a.CreatedAt, a.UpdatedAt, a.Deleted, nullableTime(a.DeletedAt))
	return dbErr(e)
}
func (m *MySQL) GetApplication(ctx context.Context, id string) (Application, error) {
	var a Application
	var deletedAt sql.NullTime
	e := m.db.QueryRowContext(ctx, "SELECT id,organization_id,created_by,name,created_at,updated_at,deleted,deleted_at FROM applications WHERE id=? AND deleted=FALSE", id).Scan(&a.ID, &a.OrganizationID, &a.CreatedBy, &a.Name, &a.CreatedAt, &a.UpdatedAt, &a.Deleted, &deletedAt)
	a.DeletedAt = scanTime(deletedAt)
	return a, dbErr(e)
}
func (m *MySQL) CreateComment(ctx context.Context, c Comment) error {
	if err := validateComment(c); err != nil {
		return err
	}
	c.CreatedAt = stamp(c.CreatedAt)
	c.UpdatedAt = stamp(c.UpdatedAt)
	_, e := m.db.ExecContext(ctx, "INSERT INTO comments(id,application_id,owner_id,content,ref_id,origin,replies,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", c.ID, c.ApplicationID, c.OwnerID, c.Content, nullable(c.RefID), nullable(c.Origin), jsonValue(c.Replies), jsonValue(c.Flags), jsonValue(c.UpVotes), jsonValue(c.DownVotes), c.CreatedAt, c.UpdatedAt, c.Deleted, nullableTime(c.DeletedAt))
	return dbErr(e)
}

const commentSelect = "SELECT id,application_id,owner_id,content,ref_id,origin,replies,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at FROM comments"

func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var ref, origin sql.NullString
	var replies, flags, ups, downs []byte
	var deletedAt sql.NullTime
	e := row.Scan(&c.ID, &c.ApplicationID, &c.OwnerID, &c.Content, &ref, &origin, &replies, &flags, &ups, &downs, &c.CreatedAt, &c.UpdatedAt, &c.Deleted, &deletedAt)
	c.DeletedAt = scanTime(deletedAt)
	c.RefID = scanPtr(ref)
	c.Origin = scanPtr(origin)
	c.Replies = decodeStrings(replies)
	c.Flags = decodeStrings(flags)
	c.UpVotes = decodeStrings(ups)
	c.DownVotes = decodeStrings(downs)
	return c, dbErr(e)
}
func (m *MySQL) GetComment(ctx context.Context, app, id string) (Comment, error) {
	return scanComment(m.db.QueryRowContext(ctx, commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE", app, id))
}
func (m *MySQL) ListComments(ctx context.Context, f Filter) ([]Comment, int, error) {
	where := []string{"application_id=?", "deleted=FALSE"}
	args := []any{f.ApplicationID}
	if f.RefID != nil {
		where = append(where, "ref_id=?")
		args = append(args, *f.RefID)
	}
	if f.Origin != nil {
		where = append(where, "origin=?")
		args = append(args, *f.Origin)
	}
	if f.OwnerID != nil {
		where = append(where, "owner_id=?")
		args = append(args, *f.OwnerID)
	}
	if f.Flagged != nil {
		where = append(where, "(JSON_LENGTH(flags)>0)=?")
		args = append(args, *f.Flagged)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if e := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE "+clause, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	order := "ASC"
	if f.Desc {
		order = "DESC"
	}
	q := commentSelect + " WHERE " + clause + " ORDER BY created_at " + order + ", id " + order
	page, limit := f.Page, f.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, limit, (page-1)*limit)
	rows, e := m.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		c, e := scanComment(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}
func (m *MySQL) UpdateComment(ctx context.Context, app, id, owner, content string) (Comment, error) {
	if err := validateText("comment content", content, maxMediumTextBytes); err != nil {
		return Comment{}, err
	}
	tx, e := m.db.BeginTx(ctx, nil)
	if e != nil {
		return Comment{}, e
	}
	c, e := scanComment(tx.QueryRowContext(ctx, commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE FOR UPDATE", app, id))
	if e != nil {
		tx.Rollback()
		return Comment{}, e
	}
	if c.OwnerID != owner {
		tx.Rollback()
		return Comment{}, ErrForbidden
	}
	_, e = tx.ExecContext(ctx, "UPDATE comments SET content=?,updated_at=? WHERE application_id=? AND id=?", content, time.Now().UTC().Truncate(time.Millisecond), app, id)
	if e != nil {
		tx.Rollback()
		return Comment{}, e
	}
	if e = tx.Commit(); e != nil {
		return Comment{}, e
	}
	c.Content = content
	return c, nil
}
func (m *MySQL) DeleteComment(ctx context.Context, app, id, owner string) (Comment, error) {
	tx, e := m.db.BeginTx(ctx, nil)
	if e != nil {
		return Comment{}, e
	}
	c, e := scanComment(tx.QueryRowContext(ctx, commentSelect+" WHERE application_id=? AND id=? AND deleted=FALSE FOR UPDATE", app, id))
	if e != nil {
		tx.Rollback()
		return Comment{}, e
	}
	if c.OwnerID != owner {
		tx.Rollback()
		return Comment{}, ErrForbidden
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, e = tx.ExecContext(ctx, "UPDATE comments SET deleted=TRUE,deleted_at=?,updated_at=? WHERE application_id=? AND id=?", now, now, app, id); e != nil {
		tx.Rollback()
		return Comment{}, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE comment_replies SET deleted=TRUE,deleted_at=?,updated_at=? WHERE comment_id=? AND deleted=FALSE", now, now, id); e != nil {
		tx.Rollback()
		return Comment{}, e
	}
	if e = tx.Commit(); e != nil {
		return Comment{}, e
	}
	return c, nil
}

// PutImported inserts a typed legacy record without replacing existing target data.
func (m *MySQL) PutImported(ctx context.Context, kind string, value any) (bool, error) {
	if err := ValidateImported(kind, value); err != nil {
		return false, err
	}
	var id string
	var query string
	var args []any
	switch kind {
	case "organizations":
		v, ok := value.(Organization)
		if !ok {
			return false, ErrConflict
		}
		if err := validateOrganization(v); err != nil {
			return false, err
		}
		id = v.ID
		query = "INSERT INTO organizations(id,name,email,secret,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?)"
		args = []any{v.ID, v.Name, v.Email, v.Secret, stamp(v.CreatedAt), stamp(v.UpdatedAt), v.Deleted, nullableTime(v.DeletedAt)}
	case "admins":
		v, ok := value.(Admin)
		if !ok {
			return false, ErrConflict
		}
		if err := validateAdmin(v); err != nil {
			return false, err
		}
		id = v.ID
		query = "INSERT INTO admins(id,organization_id,fullname,email,password,created_at,updated_at) VALUES(?,?,?,?,?,?,?)"
		args = []any{v.ID, v.OrganizationID, v.Fullname, v.Email, v.Password, stamp(v.CreatedAt), stamp(v.UpdatedAt)}
	case "applications":
		v, ok := value.(Application)
		if !ok {
			return false, ErrConflict
		}
		if err := validateApplication(v); err != nil {
			return false, err
		}
		id = v.ID
		query = "INSERT INTO applications(id,organization_id,created_by,name,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?)"
		args = []any{v.ID, v.OrganizationID, v.CreatedBy, v.Name, stamp(v.CreatedAt), stamp(v.UpdatedAt), v.Deleted, nullableTime(v.DeletedAt)}
	case "comments":
		v, ok := value.(Comment)
		if !ok {
			return false, ErrConflict
		}
		if err := validateComment(v); err != nil {
			return false, err
		}
		id = v.ID
		query = "INSERT INTO comments(id,application_id,owner_id,content,ref_id,origin,replies,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
		args = []any{v.ID, v.ApplicationID, v.OwnerID, v.Content, nullable(v.RefID), nullable(v.Origin), jsonValue(v.Replies), jsonValue(v.Flags), jsonValue(v.UpVotes), jsonValue(v.DownVotes), stamp(v.CreatedAt), stamp(v.UpdatedAt), v.Deleted, nullableTime(v.DeletedAt)}
	case "replies":
		v, ok := value.(Reply)
		if !ok {
			return false, ErrConflict
		}
		if err := validateReply(v); err != nil {
			return false, err
		}
		id = v.ID
		query = "INSERT INTO comment_replies(id,comment_id,owner_id,content,flags,up_votes,down_votes,created_at,updated_at,deleted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)"
		args = []any{v.ID, v.CommentID, v.OwnerID, v.Content, jsonValue(v.Flags), jsonValue(v.UpVotes), jsonValue(v.DownVotes), stamp(v.CreatedAt), stamp(v.UpdatedAt), v.Deleted, nullableTime(v.DeletedAt)}
	default:
		return false, ErrConflict
	}
	_, err := m.db.ExecContext(ctx, query, args...)
	if err == nil {
		return true, nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		table := map[string]string{"organizations": "organizations", "admins": "admins", "applications": "applications", "comments": "comments", "replies": "comment_replies"}[kind]
		var exists int
		e := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", id).Scan(&exists)
		if e == nil && exists > 0 {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		return false, ErrConflict
	}
	return false, dbErr(err)
}

var _ Repository = (*MySQL)(nil)
