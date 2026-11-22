package tenantagent

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Safe metadata can include expired/consumed records for diagnosis; their
// presence never means a credential is currently authorized.
type EnrollmentMetadata struct {
	ID         string     `json:"id"`
	Generation int        `json:"generation"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Details struct {
	Agent      Agent               `json:"agent"`
	Enrollment *EnrollmentMetadata `json:"enrollment,omitempty"`
	Credential *CredentialMetadata `json:"credential,omitempty"`
}

const agentColumns = "id::text,organization_id::text,name,state,revision,created_by::text,created_at,updated_at"

type scanner interface{ Scan(...any) error }

func scanAgent(row scanner) (Agent, error) {
	var a Agent
	err := row.Scan(&a.ID, &a.OrganizationID, &a.Name, &a.State, &a.Revision, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func agentByID(ctx context.Context, tx pgx.Tx, id string) (Agent, error) {
	return scanAgent(tx.QueryRow(ctx, "SELECT "+agentColumns+" FROM tenant_agents WHERE id=$1", id))
}

func detailsTx(ctx context.Context, tx pgx.Tx, org, id string) (Details, error) {
	a, err := scanAgent(tx.QueryRow(ctx, "SELECT "+agentColumns+" FROM tenant_agents WHERE organization_id=$1 AND id=$2", org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Details{}, notFound()
	}
	if err != nil {
		return Details{}, err
	}
	result := Details{Agent: a}
	var enrollment EnrollmentMetadata
	err = tx.QueryRow(ctx, `SELECT enrollment_id::text,generation,expires_at,consumed_at,revoked_at
 FROM agent_enrollments WHERE organization_id=$1 AND agent_id=$2`, org, id).Scan(&enrollment.ID, &enrollment.Generation, &enrollment.ExpiresAt, &enrollment.ConsumedAt, &enrollment.RevokedAt)
	if err == nil {
		result.Enrollment = &enrollment
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Details{}, err
	}
	var credential CredentialMetadata
	err = tx.QueryRow(ctx, `SELECT credential_id::text,generation,expires_at,revoked_at
 FROM agent_credentials WHERE organization_id=$1 AND agent_id=$2`, org, id).Scan(&credential.ID, &credential.Generation, &credential.ExpiresAt, &credential.RevokedAt)
	if err == nil {
		result.Credential = &credential
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Details{}, err
	}
	return result, nil
}
