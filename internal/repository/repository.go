// Package repository defines persistent tenant and comment storage.
package repository

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("record not found")
var ErrConflict = errors.New("record already exists")
var ErrForbidden = errors.New("owner mismatch")
var ErrInvalid = errors.New("invalid record")

type Organization struct {
	ID, Name, Email, Secret string
	CreatedAt, UpdatedAt    time.Time
	Deleted                 bool
	DeletedAt               *time.Time
}
type Admin struct {
	ID, OrganizationID, Fullname, Email, Password string
	CreatedAt, UpdatedAt                          time.Time
}
type Application struct {
	ID, OrganizationID, CreatedBy, Name string
	CreatedAt, UpdatedAt                time.Time
	Deleted                             bool
	DeletedAt                           *time.Time
}
type Comment struct {
	ID, ApplicationID, OwnerID, Content string
	RefID, Origin                       *string
	Replies, Flags, UpVotes, DownVotes  []string
	CreatedAt, UpdatedAt                time.Time
	Deleted                             bool
	DeletedAt                           *time.Time
}
type Reply struct {
	ID, CommentID, OwnerID, Content string
	Flags, UpVotes, DownVotes       []string
	CreatedAt, UpdatedAt            time.Time
	Deleted                         bool
	DeletedAt                       *time.Time
}
type Filter struct {
	ApplicationID          string
	RefID, Origin, OwnerID *string
	Flagged                *bool
	Page, Limit            int
	Desc                   bool
}

type Repository interface {
	Ping(context.Context) error
	CreateOrganization(context.Context, Organization, Admin) error
	GetOrganization(context.Context, string) (Organization, error)
	FindAdmin(context.Context, string, string) (Admin, error)
	GetAdmin(context.Context, string) (Admin, error)
	CreateApplication(context.Context, Application) error
	GetApplication(context.Context, string) (Application, error)
	CreateComment(context.Context, Comment) error
	GetComment(context.Context, string, string) (Comment, error)
	ListComments(context.Context, Filter) ([]Comment, int, error)
	UpdateComment(context.Context, string, string, string, string) (Comment, error)
	DeleteComment(context.Context, string, string, string) (Comment, error)
}
