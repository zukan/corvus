package migration

import (
	"context"
	"fmt"
	"os"
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

type stagedRecord struct {
	collection string
	index      int
	offset     int64
	length     int
}

type relationRecord struct {
	collection, id, organizationID, parentID, createdBy string
	index                                               int
}

func Run(ctx context.Context, source Source, sink Sink, dryRun bool) (map[string]Counts, error) {
	report := make(map[string]Counts, len(collections))
	var staged []stagedRecord
	var relations []relationRecord
	var spool *os.File
	if !dryRun {
		var err error
		spool, err = os.CreateTemp("", "corvus-migration-*.bson")
		if err != nil {
			return report, fmt.Errorf("preflight staging failed")
		}
		defer func() {
			_ = spool.Close()
			_ = os.Remove(spool.Name())
		}()
	}
	for _, collection := range collections {
		var count Counts
		index := 0
		var recordErr error
		err := source.Iterate(ctx, collection, func(doc bson.M) error {
			index++
			count.Read++
			report[collection] = count
			value, err := convert(collection, doc)
			if err != nil {
				recordErr = fmt.Errorf("convert %s record %d failed", collection, index)
				return recordErr
			}
			if err = repository.ValidateImported(collection, value); err != nil {
				recordErr = fmt.Errorf("validate %s record %d failed", collection, index)
				return recordErr
			}
			relation, ok := relationFor(collection, index, value)
			if ok {
				relations = append(relations, relation)
			}
			record := stagedRecord{collection: collection, index: index}
			if !dryRun {
				raw, marshalErr := bson.Marshal(doc)
				if marshalErr != nil {
					recordErr = fmt.Errorf("stage %s record %d failed", collection, index)
					return recordErr
				}
				record.offset, err = spool.Seek(0, 1)
				if err != nil {
					recordErr = fmt.Errorf("stage %s record %d failed", collection, index)
					return recordErr
				}
				record.length = len(raw)
				if _, err = spool.Write(raw); err != nil {
					recordErr = fmt.Errorf("stage %s record %d failed", collection, index)
					return recordErr
				}
			}
			staged = append(staged, record)
			return nil
		})
		if err != nil {
			if recordErr != nil {
				return report, recordErr
			}
			return report, fmt.Errorf("read %s failed", collection)
		}
		report[collection] = count
	}
	if err := validateRelationships(relations); err != nil {
		return report, err
	}
	if dryRun {
		return report, nil
	}
	if err := spool.Sync(); err != nil {
		return report, fmt.Errorf("preflight staging failed")
	}
	for _, record := range staged {
		if _, err := readStaged(spool, record); err != nil {
			return report, fmt.Errorf("stage %s record %d failed", record.collection, record.index)
		}
	}
	if err := sink.Migrate(ctx); err != nil {
		return report, fmt.Errorf("schema migration failed")
	}
	for _, record := range staged {
		value, err := readStaged(spool, record)
		if err != nil {
			return report, fmt.Errorf("stage %s record %d failed", record.collection, record.index)
		}
		inserted, err := sink.PutImported(ctx, record.collection, value)
		if err != nil {
			return report, fmt.Errorf("insert %s record %d failed", record.collection, record.index)
		}
		count := report[record.collection]
		if inserted {
			count.Inserted++
		} else {
			count.Skipped++
		}
		report[record.collection] = count
	}
	return report, nil
}

func relationFor(collection string, index int, value any) (relationRecord, bool) {
	record := relationRecord{collection: collection, index: index}
	switch v := value.(type) {
	case repository.Organization:
		record.id = v.ID
	case repository.Admin:
		record.id, record.organizationID = v.ID, v.OrganizationID
	case repository.Application:
		record.id, record.organizationID, record.createdBy = v.ID, v.OrganizationID, v.CreatedBy
	case repository.Comment:
		record.id, record.parentID = v.ID, v.ApplicationID
	case repository.Reply:
		record.id, record.parentID = v.ID, v.CommentID
	default:
		return relationRecord{}, false
	}
	return record, true
}

func validateRelationships(records []relationRecord) error {
	organizations := map[string]struct{}{}
	admins := map[string]string{}
	applications := map[string]struct{}{}
	comments := map[string]struct{}{}
	for _, record := range records {
		switch record.collection {
		case "organizations":
			organizations[record.id] = struct{}{}
		case "admins":
			admins[record.id] = record.organizationID
		case "applications":
			applications[record.id] = struct{}{}
		case "comments":
			comments[record.id] = struct{}{}
		}
	}
	for _, record := range records {
		valid := true
		switch record.collection {
		case "admins":
			_, valid = organizations[record.organizationID]
		case "applications":
			_, orgExists := organizations[record.organizationID]
			adminOrg, adminExists := admins[record.createdBy]
			valid = orgExists && adminExists && adminOrg == record.organizationID
		case "comments":
			_, valid = applications[record.parentID]
		case "replies":
			_, valid = comments[record.parentID]
		}
		if !valid {
			return fmt.Errorf("relationship %s record %d failed", record.collection, record.index)
		}
	}
	return nil
}

func readStaged(file *os.File, record stagedRecord) (any, error) {
	raw := make([]byte, record.length)
	if _, err := file.ReadAt(raw, record.offset); err != nil {
		return nil, err
	}
	var document bson.M
	if err := bson.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return convert(record.collection, document)
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
