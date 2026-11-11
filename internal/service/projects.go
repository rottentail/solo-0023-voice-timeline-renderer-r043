package service

import (
	"context"
	"strings"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
)

// CreateProject provisions a project and binds the founding actor as its owner.
func (s *Service) CreateProject(ctx context.Context, principal authz.Principal, id string, request ProjectWriteRequest) (domain.Project, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Project{}, false, err
	}
	// Project provisioning is performed by an authenticated platform actor. The
	// request carries the owner that will populate the initial membership.
	if !principal.Valid() {
		return domain.Project{}, false, domain.E(domain.KindUnauthorized, "missing_identity", "", "identity headers are required")
	}
	if err := domain.ValidateProjectID("project_id", id); err != nil {
		return domain.Project{}, false, err
	}
	name := strings.TrimSpace(request.Name)
	if err := domain.ValidateName(name); err != nil {
		return domain.Project{}, false, err
	}
	ownerID := strings.TrimSpace(request.OwnerID)
	if ownerID == "" {
		ownerID = principal.ActorID
	}
	if err := domain.ValidateActorID(ownerID); err != nil {
		return domain.Project{}, false, err
	}
	ownerRole := domain.Role(strings.TrimSpace(request.OwnerRole))
	if ownerRole == "" {
		ownerRole = domain.RoleOwner
	}
	if err := domain.ValidateRole(ownerRole); err != nil {
		return domain.Project{}, false, err
	}
	if _, err := s.repo.GetProject(id); err == nil {
		return domain.Project{}, false, domain.E(domain.KindConflict, "project_conflict", "project_id", "project identifier already exists")
	} else if appErr := domain.AsAppError(err); appErr.Kind != domain.KindNotFound {
		return domain.Project{}, false, err
	}
	now := time.Now().UTC()
	project := domain.Project{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.repo.CreateProject(project, ownerID, ownerRole, now); err != nil {
		return domain.Project{}, false, err
	}
	return project, true, nil
}

// ListProjects returns only the projects in which the authenticated actor is a
// member. Other projects are never enumerated.
func (s *Service) ListProjects(ctx context.Context, principal authz.Principal) ([]domain.Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.guard.Authenticate(principal); err != nil {
		return nil, err
	}
	all, err := s.repo.ListProjects()
	if err != nil {
		return nil, err
	}
	visible := make([]domain.Project, 0, len(all))
	for _, project := range all {
		if _, err := s.repo.GetMembership(project.ID, principal.ActorID); err != nil {
			if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
				continue
			}
			return nil, err
		}
		visible = append(visible, project)
	}
	return visible, nil
}

// PutMembership adds or updates an actor's role. Only an owner may manage
// memberships.
func (s *Service) PutMembership(ctx context.Context, principal authz.Principal, request MembershipWriteRequest) (domain.Membership, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Membership{}, false, err
	}
	if _, err := s.guard.RequireRole(principal, domain.RoleOwner); err != nil {
		return domain.Membership{}, false, err
	}
	if err := domain.ValidateActorID(request.ActorID); err != nil {
		return domain.Membership{}, false, err
	}
	role := domain.Role(request.Role)
	if err := domain.ValidateRole(role); err != nil {
		return domain.Membership{}, false, err
	}
	now := time.Now().UTC()
	existing, err := s.repo.GetMembership(principal.ProjectID, request.ActorID)
	created := false
	if err != nil {
		if appErr := domain.AsAppError(err); appErr.Kind != domain.KindNotFound {
			return domain.Membership{}, false, err
		}
		existing = domain.Membership{
			ProjectID: principal.ProjectID,
			ActorID:   request.ActorID,
			Role:      role,
			CreatedAt: now,
		}
		created = true
	}
	existing.Role = role
	existing.UpdatedAt = now
	if err := s.repo.PutMembership(existing); err != nil {
		return domain.Membership{}, false, err
	}
	return existing, created, nil
}

// GetMembership reads the caller's own role inside the principal's project.
func (s *Service) GetMembership(ctx context.Context, principal authz.Principal) (domain.Membership, error) {
	if err := ctx.Err(); err != nil {
		return domain.Membership{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guard.Authenticate(principal)
}
