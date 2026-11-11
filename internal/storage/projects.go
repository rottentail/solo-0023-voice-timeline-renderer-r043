package storage

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"voice-timeline-renderer/internal/domain"
)

type memberRecord struct {
	ActorID   string      `json:"actor_id"`
	Role      domain.Role `json:"role"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type projectRecord struct {
	Project domain.Project `json:"project"`
	Members []memberRecord `json:"members"`
}

func (repo *Repository) projectPath(id string) string {
	return repo.path("projects", id+".json")
}

// CreateProject persists a new project together with its founding owner.
func (repo *Repository) CreateProject(project domain.Project, ownerID string, ownerRole domain.Role, now time.Time) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	path := repo.projectPath(project.ID)
	if _, err := os.Stat(path); err == nil {
		return domain.E(domain.KindConflict, "project_conflict", "project_id", "project identifier already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot inspect project destination", err)
	}
	record := projectRecord{
		Project: project,
		Members: []memberRecord{{
			ActorID:   ownerID,
			Role:      ownerRole,
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
	if err := writeJSONAtomic(path, record, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write project", err)
	}
	return nil
}

func (repo *Repository) readProjectRecord(id string) (projectRecord, error) {
	var record projectRecord
	if err := readJSON(repo.projectPath(id), &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return projectRecord{}, domain.E(domain.KindNotFound, "project_not_found", "project_id", "project does not exist")
		}
		return projectRecord{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read project", err)
	}
	return record, nil
}

func (repo *Repository) GetProject(id string) (domain.Project, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	record, err := repo.readProjectRecord(id)
	if err != nil {
		return domain.Project{}, err
	}
	return record.Project, nil
}

func (repo *Repository) ListProjects() ([]domain.Project, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	entries, err := os.ReadDir(repo.path("projects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list projects", err)
	}
	projects := make([]domain.Project, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, readErr := readProjectRecordAt(repo.path("projects", entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		projects = append(projects, record.Project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return projects, nil
}

func readProjectRecordAt(path string) (projectRecord, error) {
	var record projectRecord
	if err := readJSON(path, &record); err != nil {
		return projectRecord{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read project record", err)
	}
	return record, nil
}

// GetMembership returns the role an actor holds inside a project. A missing
// project and a missing membership are intentionally surfaced through the same
// not-found error so callers cannot probe project existence.
func (repo *Repository) GetMembership(projectID, actorID string) (domain.Membership, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	record, err := repo.readProjectRecord(projectID)
	if err != nil {
		return domain.Membership{}, err
	}
	for _, member := range record.Members {
		if member.ActorID == actorID {
			return domain.Membership{
				ProjectID: record.Project.ID,
				ActorID:   member.ActorID,
				Role:      member.Role,
				CreatedAt: member.CreatedAt,
				UpdatedAt: member.UpdatedAt,
			}, nil
		}
	}
	return domain.Membership{}, domain.E(domain.KindNotFound, "membership_not_found", "actor_id", "actor is not a member of the project")
}

func (repo *Repository) PutMembership(membership domain.Membership) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	record, err := repo.readProjectRecord(membership.ProjectID)
	if err != nil {
		return err
	}
	for index, member := range record.Members {
		if member.ActorID == membership.ActorID {
			record.Members[index].Role = membership.Role
			record.Members[index].UpdatedAt = membership.UpdatedAt
			return writeJSONAtomic(repo.projectPath(membership.ProjectID), record, 0o640)
		}
	}
	record.Members = append(record.Members, memberRecord{
		ActorID:   membership.ActorID,
		Role:      membership.Role,
		CreatedAt: membership.CreatedAt,
		UpdatedAt: membership.UpdatedAt,
	})
	sort.Slice(record.Members, func(i, j int) bool { return record.Members[i].ActorID < record.Members[j].ActorID })
	if err := writeJSONAtomic(repo.projectPath(membership.ProjectID), record, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write project membership", err)
	}
	return nil
}
