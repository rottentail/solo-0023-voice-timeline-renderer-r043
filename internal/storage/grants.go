package storage

import (
	"errors"
	"os"
	"sort"
	"time"

	"voice-timeline-renderer/internal/domain"
)

type grantRegistry struct {
	Grants []domain.Grant `json:"grants"`
}

func (repo *Repository) grantRegistryPath() string {
	return repo.path("grants", "registry.json")
}

func (repo *Repository) readGrantRegistry() (grantRegistry, error) {
	var registry grantRegistry
	if err := readJSON(repo.grantRegistryPath(), &registry); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return grantRegistry{Grants: []domain.Grant{}}, nil
		}
		return grantRegistry{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read grant registry", err)
	}
	if registry.Grants == nil {
		registry.Grants = []domain.Grant{}
	}
	return registry, nil
}

func (repo *Repository) writeGrantRegistry(registry grantRegistry) error {
	sort.Slice(registry.Grants, func(i, j int) bool { return registry.Grants[i].ID < registry.Grants[j].ID })
	if err := writeJSONAtomic(repo.grantRegistryPath(), registry, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write grant registry", err)
	}
	return nil
}

func grantNotFoundError() error {
	// Grants are addressed by unguessable identifiers; an unknown grant looks
	// exactly like a non-existent resource to every caller.
	return domain.E(domain.KindNotFound, "grant_not_found", "grant_id", "grant does not exist")
}

// CreateGrant stores a new active grant. Callers validate ownership and the
// project pair before invoking this method.
func (repo *Repository) CreateGrant(grant domain.Grant) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return err
	}
	for _, existing := range registry.Grants {
		if existing.ID == grant.ID {
			return domain.E(domain.KindConflict, "grant_conflict", "grant_id", "grant identifier already exists")
		}
		if existing.State == domain.GrantActive &&
			existing.SourceID == grant.SourceID &&
			existing.OwnerProjectID == grant.OwnerProjectID &&
			existing.GranteeProjectID == grant.GranteeProjectID {
			return domain.E(domain.KindConflict, "grant_conflict", "grant_id", "an active grant already exists for this source and project pair")
		}
	}
	registry.Grants = append(registry.Grants, grant)
	return repo.writeGrantRegistry(registry)
}

func (repo *Repository) GetGrant(id string) (domain.Grant, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return domain.Grant{}, err
	}
	for _, grant := range registry.Grants {
		if grant.ID == id {
			return grant, nil
		}
	}
	return domain.Grant{}, grantNotFoundError()
}

// RevokeGrant marks a grant revoked. Revocation is terminal; revoking an
// already revoked grant is idempotent and returns the current record.
func (repo *Repository) RevokeGrant(id string, now time.Time) (domain.Grant, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return domain.Grant{}, err
	}
	for index := range registry.Grants {
		if registry.Grants[index].ID != id {
			continue
		}
		grant := registry.Grants[index]
		if grant.State == domain.GrantRevoked {
			return grant, nil
		}
		revokedAt := now.UTC()
		grant.State = domain.GrantRevoked
		grant.RevokedAt = &revokedAt
		grant.UpdatedAt = revokedAt
		registry.Grants[index] = grant
		if err := repo.writeGrantRegistry(registry); err != nil {
			return domain.Grant{}, err
		}
		return grant, nil
	}
	return domain.Grant{}, grantNotFoundError()
}

// FindActiveGrant returns the active grant that lets granteeProject consume
// sourceID, or a not-found error when no such grant exists. The not-found
// result is the same shape used for unknown grants so existence cannot be
// probed through this method.
func (repo *Repository) FindActiveGrant(sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return domain.Grant{}, err
	}
	for _, grant := range registry.Grants {
		if grant.State == domain.GrantActive &&
			grant.SourceID == sourceID &&
			grant.OwnerProjectID == ownerProjectID &&
			grant.GranteeProjectID == granteeProjectID {
			return grant, nil
		}
	}
	return domain.Grant{}, grantNotFoundError()
}

// GrantIsActive re-validates a previously captured grant by its identifier and
// source. It is the publication-time authority check: a render may commit only
// while the named grant is still active and still covers the same source and
// project pair.
func (repo *Repository) GrantIsActive(grantID, sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return domain.Grant{}, err
	}
	for _, grant := range registry.Grants {
		if grant.ID != grantID {
			continue
		}
		if grant.State == domain.GrantActive &&
			grant.SourceID == sourceID &&
			grant.OwnerProjectID == ownerProjectID &&
			grant.GranteeProjectID == granteeProjectID {
			return grant, nil
		}
		return domain.Grant{}, domain.E(domain.KindForbidden, "authorization_revoked", "grant_id", "the source grant is no longer active")
	}
	return domain.Grant{}, grantNotFoundError()
}

// ListGrantsForProject returns grants visible to a project: grants it issued as
// owner and grants it received as grantee.
func (repo *Repository) ListGrantsForProject(projectID string) ([]domain.Grant, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()

	registry, err := repo.readGrantRegistry()
	if err != nil {
		return nil, err
	}
	visible := make([]domain.Grant, 0)
	for _, grant := range registry.Grants {
		if grant.OwnerProjectID == projectID || grant.GranteeProjectID == projectID {
			visible = append(visible, grant)
		}
	}
	return visible, nil
}
