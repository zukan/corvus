package repository

import (
	"fmt"
	"unicode/utf8"
)

const (
	maxIDChars         = 191
	maxNameChars       = 255
	maxEmailChars      = 320
	maxTextBytes       = 65_535
	maxMediumTextBytes = 16_777_215
)

func validateID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	return validateVarchar(field, value, maxIDChars)
}
func validateVarchar(field, value string, max int) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%w: %s exceeds %d characters or is invalid UTF-8", ErrInvalid, field, max)
	}
	return nil
}
func validateText(field, value string, max int) error {
	if !utf8.ValidString(value) || len(value) > max {
		return fmt.Errorf("%w: %s exceeds %d bytes or is invalid UTF-8", ErrInvalid, field, max)
	}
	return nil
}
func validateOptionalText(field string, value *string) error {
	if value == nil {
		return nil
	}
	return validateText(field, *value, maxMediumTextBytes)
}
func validateOrganization(v Organization) error {
	for _, err := range []error{validateID("organization id", v.ID), validateVarchar("organization name", v.Name, maxNameChars), validateVarchar("organization email", v.Email, maxEmailChars), validateText("organization secret", v.Secret, maxTextBytes)} {
		if err != nil {
			return err
		}
	}
	return nil
}
func validateAdmin(v Admin) error {
	for _, err := range []error{validateID("admin id", v.ID), validateID("admin organization id", v.OrganizationID), validateVarchar("admin fullname", v.Fullname, maxNameChars), validateVarchar("admin email", v.Email, maxEmailChars), validateText("admin password", v.Password, maxTextBytes)} {
		if err != nil {
			return err
		}
	}
	return nil
}
func validateApplication(v Application) error {
	for _, err := range []error{validateID("application id", v.ID), validateID("application organization id", v.OrganizationID), validateVarchar("application creator id", v.CreatedBy, maxIDChars), validateVarchar("application name", v.Name, maxNameChars)} {
		if err != nil {
			return err
		}
	}
	return nil
}
func validateComment(v Comment) error {
	for _, err := range []error{validateID("comment id", v.ID), validateID("comment application id", v.ApplicationID), validateText("comment owner id", v.OwnerID, maxMediumTextBytes), validateText("comment content", v.Content, maxMediumTextBytes), validateOptionalText("comment ref id", v.RefID), validateOptionalText("comment origin", v.Origin)} {
		if err != nil {
			return err
		}
	}
	return nil
}
func validateReply(v Reply) error {
	for _, err := range []error{validateID("reply id", v.ID), validateID("reply comment id", v.CommentID), validateText("reply owner id", v.OwnerID, maxMediumTextBytes), validateText("reply content", v.Content, maxMediumTextBytes)} {
		if err != nil {
			return err
		}
	}
	return nil
}

// ValidateImported applies the same type and storage-size constraints used by repository import sinks.
func ValidateImported(kind string, value any) error {
	switch kind {
	case "organizations":
		v, ok := value.(Organization)
		if !ok {
			return ErrConflict
		}
		return validateOrganization(v)
	case "admins":
		v, ok := value.(Admin)
		if !ok {
			return ErrConflict
		}
		return validateAdmin(v)
	case "applications":
		v, ok := value.(Application)
		if !ok {
			return ErrConflict
		}
		return validateApplication(v)
	case "comments":
		v, ok := value.(Comment)
		if !ok {
			return ErrConflict
		}
		return validateComment(v)
	case "replies":
		v, ok := value.(Reply)
		if !ok {
			return ErrConflict
		}
		return validateReply(v)
	default:
		return ErrConflict
	}
}
