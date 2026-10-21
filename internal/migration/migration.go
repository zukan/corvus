package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/zukan/corvus/internal/repository"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"
)

var collections = []string{"organizations", "admins", "applications", "comments", "replies"}

type Source interface {
	Iterate(context.Context, string, func(bson.M) error) error
}
type Sink interface {
	Migrate(context.Context) error
	PutImported(context.Context, string, any) (bool, error)
}
type Counts struct {
	Read     int
	Inserted int
	Skipped  int
}

func Run(ctx context.Context, source Source, sink Sink, dryRun bool) (map[string]Counts, error) {
	report := make(map[string]Counts, len(collections))
	if !dryRun {
		if err := sink.Migrate(ctx); err != nil {
			return nil, fmt.Errorf("schema migration failed")
		}
	}
	for _, collection := range collections {
		var count Counts
		index := 0
		var recordErr error
		err := source.Iterate(ctx, collection, func(doc bson.M) error {
			index++
			count.Read++
			value, err := convert(collection, doc)
			if err != nil {
				recordErr = fmt.Errorf("convert %s record %d failed", collection, index)
				return recordErr
			}
			if err = repository.ValidateImported(collection, value); err != nil {
				recordErr = fmt.Errorf("validate %s record %d failed", collection, index)
				return recordErr
			}
			if dryRun {
				return nil
			}
			inserted, err := sink.PutImported(ctx, collection, value)
			if err != nil {
				recordErr = fmt.Errorf("insert %s record %d failed", collection, index)
				return recordErr
			}
			if inserted {
				count.Inserted++
			} else {
				count.Skipped++
			}
			return nil
		})
		if err != nil {
			if recordErr != nil {
				return nil, recordErr
			}
			return nil, fmt.Errorf("read %s failed", collection)
		}
		report[collection] = count
	}
	return report, nil
}

func convert(kind string, d bson.M) (any, error) {
	id, err := stringID(d["_id"])
	if err != nil || id == "" {
		return nil, fmt.Errorf("invalid id")
	}
	str := func(key string, required bool) (string, error) {
		v, ok := d[key].(string)
		if (!ok || v == "") && required {
			return "", fmt.Errorf("invalid %s", key)
		}
		return v, nil
	}
	ref := func(key string) (string, error) { return stringID(d[key]) }
	date := func(key string) (time.Time, error) {
		switch v := d[key].(type) {
		case time.Time:
			return v.UTC(), nil
		case bson.DateTime:
			return v.Time().UTC(), nil
		}
		return time.Time{}, fmt.Errorf("invalid %s", key)
	}
	var deletedAt *time.Time
	deleted := false
	hasDeleted := false
	if rawDeleted, exists := d["deleted"]; exists {
		value, ok := rawDeleted.(bool)
		if !ok {
			return nil, fmt.Errorf("invalid deleted")
		}
		deleted, hasDeleted = value, true
	}
	if rawDeletedAt, exists := d["deletedAt"]; exists && rawDeletedAt != nil {
		timestamp, err := date("deletedAt")
		if err != nil {
			return nil, err
		}
		if hasDeleted && !deleted {
			return nil, fmt.Errorf("inconsistent deleted state")
		}
		deleted = true
		deletedAt = &timestamp
	}
	created, err := date("createdAt")
	if err != nil {
		return nil, err
	}
	updated, err := date("updatedAt")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "organizations":
		name, e := str("name", true)
		if e != nil {
			return nil, e
		}
		email, e := str("email", true)
		if e != nil {
			return nil, e
		}
		secret, e := str("secret", true)
		if e != nil {
			return nil, e
		}
		if _, e = bcrypt.Cost([]byte(secret)); e != nil {
			return nil, fmt.Errorf("invalid secret hash")
		}
		return repository.Organization{ID: id, Name: name, Email: email, Secret: secret, CreatedAt: created, UpdatedAt: updated, Deleted: deleted, DeletedAt: deletedAt}, nil
	case "admins":
		org, e := ref("organizationId")
		if e != nil || org == "" {
			return nil, fmt.Errorf("invalid organizationId")
		}
		fullname, e := str("fullname", true)
		if e != nil {
			return nil, e
		}
		email, e := str("email", true)
		if e != nil {
			return nil, e
		}
		password, e := str("password", true)
		if e != nil {
			return nil, e
		}
		if _, e = bcrypt.Cost([]byte(password)); e != nil {
			return nil, fmt.Errorf("invalid password hash")
		}
		return repository.Admin{ID: id, OrganizationID: org, Fullname: fullname, Email: email, Password: password, CreatedAt: created, UpdatedAt: updated}, nil
	case "applications":
		org, e := ref("organizationId")
		if e != nil || org == "" {
			return nil, fmt.Errorf("invalid organizationId")
		}
		admin, e := ref("createdBy")
		if e != nil || admin == "" {
			return nil, fmt.Errorf("invalid createdBy")
		}
		name, e := str("name", true)
		if e != nil {
			return nil, e
		}
		return repository.Application{ID: id, OrganizationID: org, CreatedBy: admin, Name: name, CreatedAt: created, UpdatedAt: updated, Deleted: deleted, DeletedAt: deletedAt}, nil
	case "comments":
		app, e := ref("applicationId")
		if e != nil || app == "" {
			return nil, fmt.Errorf("invalid applicationId")
		}
		owner, e := str("ownerId", true)
		if e != nil {
			return nil, e
		}
		content, e := str("content", true)
		if e != nil {
			return nil, e
		}
		refID, e := optionalString(d, "refId")
		if e != nil {
			return nil, e
		}
		origin, e := optionalString(d, "origin")
		if e != nil {
			return nil, e
		}
		replies, e := stringIDs(d["replies"])
		if e != nil {
			return nil, e
		}
		flags, e := stringArray(d["flags"])
		if e != nil {
			return nil, e
		}
		ups, e := stringArray(d["upVotes"])
		if e != nil {
			return nil, e
		}
		downs, e := stringArray(d["downVotes"])
		if e != nil {
			return nil, e
		}
		return repository.Comment{ID: id, ApplicationID: app, OwnerID: owner, Content: content, RefID: refID, Origin: origin, Replies: replies, Flags: flags, UpVotes: ups, DownVotes: downs, CreatedAt: created, UpdatedAt: updated, Deleted: deleted, DeletedAt: deletedAt}, nil
	case "replies":
		comment, e := ref("commentId")
		if e != nil || comment == "" {
			return nil, fmt.Errorf("invalid commentId")
		}
		owner, e := str("ownerId", true)
		if e != nil {
			return nil, e
		}
		content, e := str("content", true)
		if e != nil {
			return nil, e
		}
		flags, e := stringArray(d["flags"])
		if e != nil {
			return nil, e
		}
		ups, e := stringArray(d["upVotes"])
		if e != nil {
			return nil, e
		}
		downs, e := stringArray(d["downVotes"])
		if e != nil {
			return nil, e
		}
		return repository.Reply{ID: id, CommentID: comment, OwnerID: owner, Content: content, Flags: flags, UpVotes: ups, DownVotes: downs, CreatedAt: created, UpdatedAt: updated, Deleted: deleted, DeletedAt: deletedAt}, nil
	default:
		return nil, fmt.Errorf("unknown kind")
	}
}

func stringID(v any) (string, error) {
	switch x := v.(type) {
	case bson.ObjectID:
		return x.Hex(), nil
	case string:
		if x != "" {
			return x, nil
		}
	}
	return "", fmt.Errorf("invalid id")
}
func optionalString(d bson.M, key string) (*string, error) {
	v, ok := d[key]
	if !ok || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("invalid %s", key)
	}
	return &s, nil
}
func stringArray(v any) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	switch values := v.(type) {
	case bson.A:
		out := make([]string, 0, len(values))
		for _, x := range values {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("invalid array")
			}
			out = append(out, s)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(values))
		for _, x := range values {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("invalid array")
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return values, nil
	}
	return nil, fmt.Errorf("invalid array")
}
func stringIDs(v any) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	switch values := v.(type) {
	case bson.A:
		out := make([]string, 0, len(values))
		for _, x := range values {
			s, e := stringID(x)
			if e != nil {
				return nil, e
			}
			out = append(out, s)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(values))
		for _, x := range values {
			s, e := stringID(x)
			if e != nil {
				return nil, e
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("invalid ids")
}
