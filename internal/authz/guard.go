// Package authz is the single permission authority for project ownership and
// cross-project source grants. Service workflows resolve every resource access
// through a Guard so that HTTP entry points and internal cross-references use
// the same permission facts rather than separate checks.
package authz

import (
	"voice-timeline-renderer/internal/domain"
)

// Principal is the authenticated caller attached to each request.
type Principal struct {
	ProjectID string
	ActorID   string
}

func (principal Principal) Valid() bool {
	return principal.ProjectID != "" && principal.ActorID != ""
}

// IdentityStore exposes the membership and grant facts a Guard needs. The
// storage repository implements this interface.
type IdentityStore interface {
	GetProject(projectID string) (domain.Project, error)
	GetMembership(projectID, actorID string) (domain.Membership, error)
	FindActiveGrant(sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error)
	GrantIsActive(grantID, sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error)
}

// Guard evaluates permission decisions for one service instance.
type Guard struct {
	store IdentityStore
}

func NewGuard(store IdentityStore) *Guard {
	return &Guard{store: store}
}

// Authenticate resolves a principal into its project role. An unknown project
// and a non-member produce the same not-found error so project existence is
// never disclosed to a caller without membership.
func (guard *Guard) Authenticate(principal Principal) (domain.Membership, error) {
	if !principal.Valid() {
		return domain.Membership{}, domain.E(domain.KindUnauthorized, "missing_identity", "", "project_id and actor_id identity headers are required")
	}
	if err := domain.ValidateProjectID("project_id", principal.ProjectID); err != nil {
		return domain.Membership{}, domain.E(domain.KindUnauthorized, "invalid_identity", "project_id", "project identity header is invalid")
	}
	if err := domain.ValidateActorID(principal.ActorID); err != nil {
		return domain.Membership{}, domain.E(domain.KindUnauthorized, "invalid_identity", "actor_id", "actor identity header is invalid")
	}
	// Look up the project first but never distinguish its absence from a
	// missing membership.
	if _, err := guard.store.GetProject(principal.ProjectID); err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
			return domain.Membership{}, domain.E(domain.KindNotFound, "project_not_found", "project_id", "project does not exist or the actor is not a member")
		}
		return domain.Membership{}, err
	}
	membership, err := guard.store.GetMembership(principal.ProjectID, principal.ActorID)
	if err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
			return domain.Membership{}, domain.E(domain.KindNotFound, "project_not_found", "project_id", "project does not exist or the actor is not a member")
		}
		return domain.Membership{}, err
	}
	return membership, nil
}

// RequireRole enforces that the principal is a member of the principal's own
// project with at least the required role.
func (guard *Guard) RequireRole(principal Principal, required domain.Role) (domain.Membership, error) {
	membership, err := guard.Authenticate(principal)
	if err != nil {
		return domain.Membership{}, err
	}
	if !membership.Role.Permits(required) {
		return domain.Membership{}, domain.E(domain.KindForbidden, "project_access_denied", "role", "actor role is not sufficient for this operation")
	}
	return membership, nil
}

// AuthorizeSourceRead decides whether the principal's project may read a
// source. Owners read their own sources; every other project needs an active
// grant. A denied read returns a source-not-found error so a project can never
// learn whether another project's source exists.
func (guard *Guard) AuthorizeSourceRead(principal Principal, asset domain.SourceAsset) (domain.SourceAuthorization, error) {
	if _, err := guard.Authenticate(principal); err != nil {
		return domain.SourceAuthorization{}, err
	}
	if asset.ProjectID == "" {
		// Unowned legacy records are invisible until an explicit adoption gives
		// them a project; they must not be reachable through cross references.
		return domain.SourceAuthorization{}, sourceNotFound(asset.ID)
	}
	if asset.ProjectID == principal.ProjectID {
		return domain.SourceAuthorization{
			SourceID:       asset.ID,
			OwnerProjectID: asset.ProjectID,
			AccessBasis:    domain.AccessBasisOwned,
		}, nil
	}
	grant, err := guard.store.FindActiveGrant(asset.ID, asset.ProjectID, principal.ProjectID)
	if err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
			return domain.SourceAuthorization{}, sourceNotFound(asset.ID)
		}
		return domain.SourceAuthorization{}, err
	}
	return domain.SourceAuthorization{
		SourceID:         asset.ID,
		OwnerProjectID:   asset.ProjectID,
		GrantID:          grant.ID,
		AccessBasis:      domain.AccessBasisGrant,
		GranteeProjectID: principal.ProjectID,
	}, nil
}

// AuthorizeSourceUse resolves read authorization for a source referenced while
// building a timeline. Timeline composition requires editor access in the
// consuming project; the returned snapshot is later embedded in render
// receipts.
func (guard *Guard) AuthorizeSourceUse(principal Principal, asset domain.SourceAsset) (domain.SourceAuthorization, error) {
	if _, err := guard.RequireRole(principal, domain.RoleEditor); err != nil {
		return domain.SourceAuthorization{}, err
	}
	return guard.AuthorizeSourceRead(principal, asset)
}

// VerifyPublication re-checks every captured authorization immediately before a
// render's artifacts are committed. Owned sources stay valid; grant-based uses
// must still point at an active grant covering the same source and project
// pair. A revoked grant fails the publication so no artifact is released with
// an authorization basis that was withdrawn first.
func (guard *Guard) VerifyPublication(principal Principal, authorizations []domain.SourceAuthorization) error {
	if _, err := guard.Authenticate(principal); err != nil {
		return err
	}
	for _, basis := range authorizations {
		switch basis.AccessBasis {
		case domain.AccessBasisOwned:
			if basis.OwnerProjectID != principal.ProjectID {
				return domain.E(domain.KindForbidden, "authorization_revoked", "grant_id", "a render source is no longer owned by the project")
			}
		case domain.AccessBasisGrant:
			if _, err := guard.store.GrantIsActive(basis.GrantID, basis.SourceID, basis.OwnerProjectID, principal.ProjectID); err != nil {
				if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
					return domain.E(domain.KindForbidden, "authorization_revoked", "grant_id", "the source grant no longer exists")
				}
				return err
			}
		default:
			return domain.E(domain.KindInternal, "invalid_authorization_basis", "access_basis", "render captured an unknown authorization basis")
		}
	}
	return nil
}

// AuthorizeResourceRead enforces that a project-owned resource (timeline or
// render) belongs to the principal's project. Cross-project reads return the
// resource's not-found error, keeping existence hidden.
func AuthorizeResourceRead(principal Principal, projectID, resourceID, code, field, message string) error {
	if projectID == "" || projectID != principal.ProjectID {
		return domain.E(domain.KindNotFound, code, field, message)
	}
	return nil
}

func sourceNotFound(sourceID string) error {
	return domain.E(domain.KindNotFound, "source_not_found", "source_id", "source does not exist or is not accessible to the project")
}
