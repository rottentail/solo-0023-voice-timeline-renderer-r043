package storage

import (
	"errors"
	"os"
	"sort"
	"time"

	"voice-timeline-renderer/internal/domain"
)

// ListLegacyRecords returns records that carry no project ownership. It is the
// single enumeration used by the migration command and migration workflow.
func (repo *Repository) ListLegacyRecords() (domain.LegacyInventory, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()

	inventory := domain.LegacyInventory{
		SourceIDs:   make([]string, 0),
		TimelineIDs: make([]string, 0),
		JobIDs:      make([]string, 0),
	}

	sourceEntries, err := os.ReadDir(repo.path("sources"))
	if err != nil {
		return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list sources", err)
	}
	for _, entry := range sourceEntries {
		if !entry.IsDir() {
			continue
		}
		var asset domain.SourceAsset
		if readErr := readJSON(repo.path("sources", entry.Name(), "metadata.json"), &asset); readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot inspect legacy source", readErr)
		}
		if asset.ProjectID == "" {
			inventory.SourceIDs = append(inventory.SourceIDs, entry.Name())
		}
	}

	timelineEntries, err := os.ReadDir(repo.path("timelines"))
	if err != nil {
		return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list timelines", err)
	}
	for _, entry := range timelineEntries {
		var plan domain.Timeline
		if readErr := readJSON(repo.path("timelines", entry.Name()), &plan); readErr != nil {
			return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot inspect legacy timeline", readErr)
		}
		if plan.ProjectID == "" {
			inventory.TimelineIDs = append(inventory.TimelineIDs, plan.ID)
		}
	}

	jobEntries, err := os.ReadDir(repo.path("jobs"))
	if err != nil {
		return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list jobs", err)
	}
	for _, entry := range jobEntries {
		var job domain.RenderJob
		if readErr := readJSON(repo.path("jobs", entry.Name()), &job); readErr != nil {
			return domain.LegacyInventory{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot inspect legacy job", readErr)
		}
		if job.ProjectID == "" {
			inventory.JobIDs = append(inventory.JobIDs, job.ID)
		}
	}

	sort.Strings(inventory.SourceIDs)
	sort.Strings(inventory.TimelineIDs)
	sort.Strings(inventory.JobIDs)
	return inventory, nil
}

type adoptionManifest = domain.AdoptionManifest

// AdoptLegacyRecords stamps explicit project ownership onto every unowned
// record and writes a durable manifest. Owned records are never rewritten. The
// render receipts of completed legacy jobs are also rewritten so their
// historical authorization remains explainable after migration.
func (repo *Repository) AdoptLegacyRecords(projectID string, now time.Time) (domain.AdoptionManifest, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	if _, err := readProjectRecordAt(repo.projectPath(projectID)); err != nil {
		return domain.AdoptionManifest{}, err
	}
	manifest := adoptionManifest{
		ProjectID:   projectID,
		AdoptedAt:   now.UTC(),
		SourceIDs:   make([]string, 0),
		TimelineIDs: make([]string, 0),
		JobIDs:      make([]string, 0),
	}

	sourceEntries, err := os.ReadDir(repo.path("sources"))
	if err != nil {
		return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list sources", err)
	}
	for _, entry := range sourceEntries {
		if !entry.IsDir() {
			continue
		}
		metaPath := repo.path("sources", entry.Name(), "metadata.json")
		var asset domain.SourceAsset
		if readErr := readJSON(metaPath, &asset); readErr != nil {
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read source for adoption", readErr)
		}
		if asset.ProjectID != "" {
			continue
		}
		asset.ProjectID = projectID
		if err := writeJSONAtomic(metaPath, asset, 0o640); err != nil {
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot adopt source", err)
		}
		manifest.SourceIDs = append(manifest.SourceIDs, asset.ID)
	}

	timelineEntries, err := os.ReadDir(repo.path("timelines"))
	if err != nil {
		return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list timelines", err)
	}
	for _, entry := range timelineEntries {
		path := repo.path("timelines", entry.Name())
		var plan domain.Timeline
		if readErr := readJSON(path, &plan); readErr != nil {
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read timeline for adoption", readErr)
		}
		if plan.ProjectID != "" {
			continue
		}
		plan.ProjectID = projectID
		if err := writeJSONAtomic(path, plan, 0o640); err != nil {
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot adopt timeline", err)
		}
		manifest.TimelineIDs = append(manifest.TimelineIDs, plan.ID)
	}

	jobEntries, err := os.ReadDir(repo.path("jobs"))
	if err != nil {
		return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot list jobs", err)
	}
	for _, entry := range jobEntries {
		jobPath := repo.path("jobs", entry.Name())
		var job domain.RenderJob
		if readErr := readJSON(jobPath, &job); readErr != nil {
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read job for adoption", readErr)
		}
		alreadyOwned := job.ProjectID != ""
		if !alreadyOwned {
			job.ProjectID = projectID
			if err := writeJSONAtomic(jobPath, job, 0o640); err != nil {
				return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot adopt render job", err)
			}
			manifest.JobIDs = append(manifest.JobIDs, job.ID)
		}
		receiptPath := repo.path("renders", job.ID, "receipt.json")
		var receipt domain.RenderReceipt
		if readErr := readJSON(receiptPath, &receipt); readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read render receipt for adoption", readErr)
		}
		if receipt.ProjectID == "" {
			receipt.ProjectID = projectID
			receipt = explainAdoptedReceipt(receipt, projectID)
			if err := writeJSONAtomic(receiptPath, receipt, 0o640); err != nil {
				return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot adopt render receipt", err)
			}
		}
	}

	sort.Strings(manifest.SourceIDs)
	sort.Strings(manifest.TimelineIDs)
	sort.Strings(manifest.JobIDs)
	manifestPath := repo.path("legacy", "adoption-"+now.UTC().Format("20060102T150405.000000000Z")+".json")
	if err := writeJSONAtomic(manifestPath, manifest, 0o640); err != nil {
		return domain.AdoptionManifest{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write adoption manifest", err)
	}
	return manifest, nil
}

// explainAdoptedReceipt backfills the authorization section of a receipt that
// existed before multi-project isolation. Every referenced source is recorded
// as owned by the adopting project at adoption time, which preserves the
// explanation of a historically legal use without altering source facts.
func explainAdoptedReceipt(receipt domain.RenderReceipt, projectID string) domain.RenderReceipt {
	seen := make(map[string]bool)
	authorizations := make([]domain.SourceAuthorization, 0, len(receipt.Spans))
	for index := range receipt.Spans {
		span := &receipt.Spans[index]
		if span.SourceProjectID == "" {
			span.SourceProjectID = projectID
		}
		if span.Authorization.SourceID == "" {
			span.Authorization = domain.SourceAuthorization{
				SourceID:       span.SourceID,
				OwnerProjectID: projectID,
				AccessBasis:    domain.AccessBasisOwned,
			}
		}
		if !seen[span.SourceID] {
			seen[span.SourceID] = true
			authorizations = append(authorizations, span.Authorization)
		}
	}
	if len(receipt.SourceAuthorizations) == 0 {
		receipt.SourceAuthorizations = authorizations
	}
	return receipt
}
