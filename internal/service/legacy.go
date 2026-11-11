package service

import (
	"context"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
)

// ListLegacy records requires owner rights and enumerates only records lacking
// explicit project ownership. It never lists another project's objects.
func (s *Service) ListLegacy(ctx context.Context, principal authz.Principal) (domain.LegacyInventory, error) {
	if err := ctx.Err(); err != nil {
		return domain.LegacyInventory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.guard.RequireRole(principal, domain.RoleOwner); err != nil {
		return domain.LegacyInventory{}, err
	}
	return s.repo.ListLegacyRecords()
}

// AdoptLegacy assigns every unowned record to the authenticated project in one
// durable pass and backfills historical render receipts. Owned records,
// including those belonging to other projects, are never touched.
func (s *Service) AdoptLegacy(ctx context.Context, principal authz.Principal) (domain.AdoptionManifest, error) {
	if err := ctx.Err(); err != nil {
		return domain.AdoptionManifest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.guard.RequireRole(principal, domain.RoleOwner); err != nil {
		return domain.AdoptionManifest{}, err
	}
	return s.repo.AdoptLegacyRecords(principal.ProjectID, time.Now().UTC())
}
