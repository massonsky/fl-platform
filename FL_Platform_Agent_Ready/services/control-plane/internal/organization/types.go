package organization

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Organization struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Kind             string    `json:"kind"`
	State            string    `json:"state"`
	Revision         int       `json:"revision"`
	CreatedAt        time.Time `json:"created_at"`
	Role             string    `json:"role"`
	MembershipStatus string    `json:"membership_status"`
	CanManageMembers bool      `json:"can_manage_members"`
}

type Membership struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	Revision    int       `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type MemberChange struct {
	Role     string `json:"role"`
	Status   string `json:"status"`
	Revision int    `json:"revision"`
}

type Failure struct {
	Status int
	Code   string
}

func (e Failure) Error() string             { return e.Code }
func failure(status int, code string) error { return Failure{status, code} }

func validIDs(ids ...string) bool {
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil {
			return false
		}
	}
	return true
}

func normalizeName(name string) (string, error) {
	if !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", failure(400, "INVALID_REQUEST")
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return "", failure(400, "INVALID_REQUEST")
	}
	return name, nil
}

func validChange(in MemberChange) bool {
	role := in.Role == "owner" || in.Role == "admin" || in.Role == "researcher" || in.Role == "viewer"
	status := in.Status == "active" || in.Status == "suspended" || in.Status == "revoked"
	return role && status && in.Revision > 0
}
