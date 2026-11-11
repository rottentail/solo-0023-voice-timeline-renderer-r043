package domain

import "time"

// Role ranks what an actor may do inside a project. The zero value is
// intentionally invalid so an unauthenticated principal never looks like a
// viewer.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

func (role Role) Valid() bool {
	switch role {
	case RoleOwner, RoleEditor, RoleViewer:
		return true
	default:
		return false
	}
}

// roleRank orders permissions; a higher rank includes every lower rank.
func (role Role) rank() int {
	switch role {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

func (role Role) Permits(required Role) bool {
	return role.Valid() && required.Valid() && role.rank() >= required.rank()
}

// Project is the ownership boundary. Every source, timeline, and render job is
// owned by exactly one project, and projects are isolated from each other
// unless an explicit source grant exists.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Membership binds an actor to a project with a single role.
type Membership struct {
	ProjectID string    `json:"project_id"`
	ActorID   string    `json:"actor_id"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GrantState tracks the lifecycle of a cross-project read-only source grant.
type GrantState string

const (
	GrantActive  GrantState = "active"
	GrantRevoked GrantState = "revoked"
)

// Grant is issued by the project that owns a source and lets another project
// read that source and place it inside its own timelines. A grant never
// conveys write access: the owning project remains the only authority over
// the source's facts. Revocation is terminal and is enforced before any render
// artifact is published.
type Grant struct {
	ID               string     `json:"id"`
	SourceID         string     `json:"source_id"`
	OwnerProjectID   string     `json:"owner_project_id"`
	GranteeProjectID string     `json:"grantee_project_id"`
	State            GrantState `json:"state"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

// SourceAuthorization is the immutable permission snapshot captured when a
// timeline references a source and again when a render is published. It is the
// single permission fact reused by every cross-project read path.
type SourceAuthorization struct {
	SourceID         string `json:"source_id"`
	OwnerProjectID   string `json:"owner_project_id"`
	GrantID          string `json:"grant_id,omitempty"`
	AccessBasis      string `json:"access_basis"`
	GranteeProjectID string `json:"grantee_project_id,omitempty"`
}

const (
	// AccessBasisOwned means the consuming project owns the source.
	AccessBasisOwned = "owned"
	// AccessBasisGrant means the consuming project holds an active grant.
	AccessBasisGrant = "grant"
)

func ValidateProjectID(field, value string) error {
	return ValidateResourceID(field, value)
}

func ValidateActorID(value string) error {
	return ValidateResourceID("actor_id", value)
}

func ValidateRole(value Role) error {
	if !value.Valid() {
		return E(KindInvalid, "invalid_role", "role", "role must be one of owner, editor, viewer")
	}
	return nil
}

func ValidateGrantState(value GrantState) error {
	switch value {
	case GrantActive, GrantRevoked:
		return nil
	default:
		return E(KindInvalid, "invalid_grant_state", "state", "grant state must be active or revoked")
	}
}

// LegacyInventory enumerates records that predate project ownership and are
// therefore inaccessible until an explicit adoption assigns them to a project.
type LegacyInventory struct {
	SourceIDs   []string `json:"source_ids"`
	TimelineIDs []string `json:"timeline_ids"`
	JobIDs      []string `json:"job_ids"`
}

// AdoptionManifest is the durable record of one legacy ownership migration.
type AdoptionManifest struct {
	ProjectID   string    `json:"project_id"`
	AdoptedAt   time.Time `json:"adopted_at"`
	SourceIDs   []string  `json:"source_ids"`
	TimelineIDs []string  `json:"timeline_ids"`
	JobIDs      []string  `json:"job_ids"`
}
