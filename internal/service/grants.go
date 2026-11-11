package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
)

// CreateGrant lets the project that owns a source grant read-only use of that
// source to another project. The grant never conveys write access and the
// source's owning project remains the only authority over its facts.
func (s *Service) CreateGrant(ctx context.Context, principal authz.Principal, request GrantWriteRequest) (domain.Grant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Grant{}, false, err
	}
	// Issuing a delegation is an owner-level action inside the owning project.
	if _, err := s.guard.RequireRole(principal, domain.RoleOwner); err != nil {
		return domain.Grant{}, false, err
	}
	if err := domain.ValidateResourceID("source_id", request.SourceID); err != nil {
		return domain.Grant{}, false, err
	}
	if err := domain.ValidateProjectID("grantee_project_id", request.GranteeProjectID); err != nil {
		return domain.Grant{}, false, err
	}
	if request.GranteeProjectID == principal.ProjectID {
		return domain.Grant{}, false, domain.E(domain.KindInvalid, "invalid_grant_target", "grantee_project_id", "a project does not need a grant to its own source")
	}
	// The owner project on the request must match the authenticated project; a
	// caller cannot issue a grant on behalf of another project.
	if request.OwnerProjectID != "" && request.OwnerProjectID != principal.ProjectID {
		return domain.Grant{}, false, domain.E(domain.KindForbidden, "grant_access_denied", "owner_project_id", "only the project that owns a source may grant it")
	}
	asset, err := s.repo.GetSourceMeta(request.SourceID)
	if err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
			return domain.Grant{}, false, domain.E(domain.KindNotFound, "source_not_found", "source_id", "source does not exist or is not accessible to the project")
		}
		return domain.Grant{}, false, err
	}
	if asset.ProjectID != principal.ProjectID {
		// Hide other projects' sources entirely.
		return domain.Grant{}, false, domain.E(domain.KindNotFound, "source_not_found", "source_id", "source does not exist or is not accessible to the project")
	}
	if _, err := s.repo.GetProject(request.GranteeProjectID); err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
			return domain.Grant{}, false, domain.E(domain.KindNotFound, "project_not_found", "grantee_project_id", "grantee project does not exist")
		}
		return domain.Grant{}, false, err
	}
	// A duplicate active pair is rejected so a grant identifier maps to exactly
	// one live delegation; revoked pairs may be re-granted with a new id.
	if _, err := s.repo.FindActiveGrant(request.SourceID, principal.ProjectID, request.GranteeProjectID); err == nil {
		return domain.Grant{}, false, domain.E(domain.KindConflict, "grant_conflict", "grant_id", "an active grant already exists for this source and project pair")
	} else if appErr := domain.AsAppError(err); appErr.Kind != domain.KindNotFound {
		return domain.Grant{}, false, err
	}
	now := time.Now().UTC()
	grant := domain.Grant{
		ID:               newGrantID(request.SourceID, request.GranteeProjectID, now),
		SourceID:         request.SourceID,
		OwnerProjectID:   principal.ProjectID,
		GranteeProjectID: request.GranteeProjectID,
		State:            domain.GrantActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.repo.CreateGrant(grant); err != nil {
		return domain.Grant{}, false, err
	}
	return grant, true, nil
}

// RevokeGrant withdraws a delegation. Only the project that issued it may
// revoke. Revocation is terminal and is enforced by the publication gate on
// every in-flight render.
func (s *Service) RevokeGrant(ctx context.Context, principal authz.Principal, grantID string) (domain.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Grant{}, err
	}
	if _, err := s.guard.RequireRole(principal, domain.RoleOwner); err != nil {
		return domain.Grant{}, err
	}
	grant, err := s.repo.GetGrant(grantID)
	if err != nil {
		return domain.Grant{}, err
	}
	if grant.OwnerProjectID != principal.ProjectID {
		return domain.Grant{}, domain.E(domain.KindNotFound, "grant_not_found", "grant_id", "grant does not exist or is not accessible to the project")
	}
	return s.repo.RevokeGrant(grantID, time.Now().UTC())
}

// ListGrants returns the grants visible to the authenticated project: grants
// it issued and grants it received.
func (s *Service) ListGrants(ctx context.Context, principal authz.Principal) ([]domain.Grant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.guard.Authenticate(principal); err != nil {
		return nil, err
	}
	return s.repo.ListGrantsForProject(principal.ProjectID)
}

// GetGrant reads a single grant visible to either the issuing or the receiving
// project. Any other project receives a not-found result.
func (s *Service) GetGrant(ctx context.Context, principal authz.Principal, grantID string) (domain.Grant, error) {
	if err := ctx.Err(); err != nil {
		return domain.Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.guard.Authenticate(principal); err != nil {
		return domain.Grant{}, err
	}
	grant, err := s.repo.GetGrant(grantID)
	if err != nil {
		return domain.Grant{}, err
	}
	if grant.OwnerProjectID != principal.ProjectID && grant.GranteeProjectID != principal.ProjectID {
		return domain.Grant{}, domain.E(domain.KindNotFound, "grant_not_found", "grant_id", "grant does not exist or is not accessible to the project")
	}
	return grant, nil
}

func newGrantID(sourceID, granteeProjectID string, now time.Time) string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		// rand.Read failing is not recoverable for a secure identifier.
		panic("cannot generate grant identifier: " + err.Error())
	}
	material := make([]byte, 0, len(sourceID)+len(granteeProjectID)+16+8)
	material = append(material, sourceID...)
	material = append(material, granteeProjectID...)
	material = append(material, byte(0))
	material = append(material, random...)
	unixNano := now.UnixNano()
	for shift := 56; shift >= 0; shift -= 8 {
		material = append(material, byte(unixNano>>shift))
	}
	digest := sha256.Sum256(material)
	return "grant_" + hex.EncodeToString(digest[:16])
}
