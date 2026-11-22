package projectaccess

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Failure struct {
	Status int
	Code   string
}

func (e Failure) Error() string             { return e.Code }
func failure(status int, code string) error { return Failure{status, code} }

type Capabilities struct {
	Read             bool `json:"can_read"`
	Export           bool `json:"can_export"`
	CreateExperiment bool `json:"can_create_experiments"`
	StartRun         bool `json:"can_start_runs"`
	CancelRun        bool `json:"can_cancel_runs"`
	ManageAccess     bool `json:"can_manage_access"`
}
type Project struct {
	ID               string       `json:"id"`
	OrganizationID   string       `json:"organization_id,omitempty"`
	OwnerID          string       `json:"owner_id"`
	CreatedBy        string       `json:"created_by"`
	Slug             string       `json:"slug"`
	Name             string       `json:"name"`
	AccessMode       string       `json:"access_mode"`
	Visibility       string       `json:"visibility"`
	ACLRevision      int          `json:"acl_revision"`
	CreatedAt        time.Time    `json:"created_at"`
	Capabilities     Capabilities `json:"capabilities"`
	AccessSource     string       `json:"access_source"`
	RunBlockedReason string       `json:"run_blocked_reason,omitempty"`
}
type ResourceRef struct{ Kind, ID, OrganizationID string }
type CreateInput struct{ Slug, Name string }
type GrantInput struct {
	Role, Status string
	Revision     int
}
type Grant struct {
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Revision  int       `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}
type VisibilityInput struct {
	Visibility  string
	ACLRevision int
}
type Action string

const (
	Read             Action = "read"
	Export           Action = "export"
	CreateExperiment Action = "create_experiment"
	QueueRun         Action = "queue_run"
	CancelRun        Action = "cancel_run"
	ManageAccess     Action = "manage_access"
)

type Policy struct {
	Mode, GlobalRole, OrganizationKind, OrganizationRole, OrganizationState string
	MembershipStatus, GrantRole, GrantStatus, Visibility                    string
	Disabled, Owner, Creator                                                bool
}

func Evaluate(in Policy) (Capabilities, string) {
	validGlobal := in.GlobalRole == "admin" || in.GlobalRole == "researcher" || in.GlobalRole == "viewer"
	if !validGlobal || in.Disabled {
		return Capabilities{}, "none"
	}
	if in.Mode == "legacy_private" {
		if !in.Owner {
			return Capabilities{}, "none"
		}
		write := in.GlobalRole != "viewer"
		return Capabilities{true, true, write, write, write, false}, "legacy_owner"
	}
	validRole := in.OrganizationRole == "owner" || in.OrganizationRole == "admin" || in.OrganizationRole == "researcher" || in.OrganizationRole == "viewer"
	if in.Mode != "organization" || in.OrganizationKind != "shared" || !validRole || in.MembershipStatus != "active" || (in.OrganizationState != "active" && in.OrganizationState != "suspended") {
		return Capabilities{}, "none"
	}
	admin := in.OrganizationRole == "owner" || in.OrganizationRole == "admin"
	read, candidateWrite, source := false, false, "none"
	switch {
	case admin:
		read, candidateWrite, source = true, true, "organization_admin"
	case in.GrantStatus == "revoked":
		source = "explicit_denied"
	case in.GrantStatus == "active" && in.GrantRole == "viewer":
		read, source = true, "explicit_viewer"
	case in.GrantStatus == "active" && in.GrantRole == "researcher":
		read, candidateWrite, source = true, true, "explicit_researcher"
	case in.GrantStatus != "":
		source = "none"
	case in.Creator:
		read, candidateWrite, source = true, true, "creator"
	case in.Visibility == "organization":
		read, source = true, "organization_read"
	}
	write := read && candidateWrite && in.GlobalRole != "viewer" && in.OrganizationRole != "viewer" && in.OrganizationState == "active"
	return Capabilities{read, read, write, false, write, write && admin}, source
}

func allowed(p Project, action Action) error {
	if !p.Capabilities.Read {
		return failure(404, "NOT_FOUND")
	}
	ok := false
	switch action {
	case Read:
		ok = true
	case Export:
		ok = p.Capabilities.Export
	case CreateExperiment:
		ok = p.Capabilities.CreateExperiment
	case CancelRun:
		ok = p.Capabilities.CancelRun
	case ManageAccess:
		ok = p.Capabilities.ManageAccess
	case QueueRun:
		if p.AccessMode == "organization" && p.Capabilities.CreateExperiment {
			return failure(503, "ORG_AGENTS_NOT_READY")
		}
		ok = p.Capabilities.StartRun
	}
	if !ok {
		return failure(403, "FORBIDDEN")
	}
	return nil
}
func validIDs(ids ...string) bool {
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil {
			return false
		}
	}
	return true
}
func validateCreate(in CreateInput) (CreateInput, error) {
	if !utf8.ValidString(in.Name) || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return in, failure(400, "INVALID_REQUEST")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || !regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`).MatchString(in.Slug) {
		return in, failure(400, "INVALID_REQUEST")
	}
	return in, nil
}
