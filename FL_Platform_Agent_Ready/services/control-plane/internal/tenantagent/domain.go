// Package tenantagent is a closed foundation, not a registration or task API.
// It never accepts or rewrites the five legacy lab client identities.
package tenantagent

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	EnrollmentTTL = 15 * time.Minute
	CredentialTTL = 30 * 24 * time.Hour
	IdentityLimit = 50
	IssueUnits    = 20
	IssueCost     = 2 // Reserve enrollment AND its eventual one-use credential.
	IssueWindow   = time.Hour
	StatePending  = "pending"
	StateActive   = "active"
	StateDisabled = "disabled"
)

// Agent is safe metadata. Storage slots, digests and issued secrets are omitted
// by construction; registry_slot must never become a scientific client_index.
type Agent struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	State          string    `json:"state"`
	Revision       int       `json:"revision"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CredentialMetadata struct {
	ID         string     `json:"id"`
	Generation int        `json:"generation"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Failure has no fields into which a secret, SQL error or claimed ID is copied.
// Transport mapping and retry semantics belong to the later service/HTTP slice.
type Failure struct {
	Status     int
	Code       string
	RetryAfter int
}

func (f Failure) Error() string { return f.Code }
func unavailable() error        { return Failure{503, "DEPENDENCY_UNAVAILABLE", 3} }
func invalid() error            { return Failure{400, "INVALID_REQUEST", 0} }
func unauthorized() error       { return Failure{401, "UNAUTHORIZED", 0} }

func normalizedName(value string) (string, error) {
	if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", invalid()
	}
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 120 {
		return "", invalid()
	}
	return value, nil
}
