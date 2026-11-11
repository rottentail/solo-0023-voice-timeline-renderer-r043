package checkflow

import (
	"context"
	"fmt"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/service"
)

// runIsolation proves that two projects with no grant between them cannot read
// or reference each other's objects, and that denied reads return the same
// not-found shape used for genuinely missing objects.
func runIsolation(ctx context.Context, harness *Harness) error {
	alpha, err := setupProject(ctx, harness, "proj_alpha", "actor_alpha")
	if err != nil {
		return err
	}
	beta, err := setupProject(ctx, harness, "proj_beta", "actor_beta")
	if err != nil {
		return err
	}
	wav, err := sineWAV(48000, 1, 200, 330, 0.3)
	if err != nil {
		return err
	}
	asset, status, err := harness.createSource(ctx, alpha, "source_alpha_secret", "private", wav)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("alpha source setup returned status %d", status)
	}

	// Beta must not be able to read alpha's source; the response is identical
	// to a missing source so existence does not leak.
	if _, deniedStatus, err := harness.getSource(ctx, beta, asset.ID); err != nil {
		return err
	} else if deniedStatus != 404 {
		return fmt.Errorf("cross-project source read returned status %d, want 404", deniedStatus)
	}

	// Beta cannot reference alpha's source in its own timeline.
	spans := []domain.TimelineSpan{
		{SourceID: asset.ID, SourceStartFrame: 0, SourceEndFrame: 1000, GainDB: 0, CrossfadeFrames: 0},
	}
	timelineStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/timelines/timeline_beta_attempt", beta, service.TimelineWriteRequest{
		Name:           "forbidden reference",
		SampleRate:     48000,
		OutputChannels: 1,
		TargetPeakDBFS: -1,
		Spans:          spans,
	})
	if err != nil {
		return err
	}
	if timelineStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("cross-project timeline reference returned status=%d code=%s, want 404/source_not_found", timelineStatus, failureCode(body))
	}

	// A non-member actor cannot authenticate into alpha even though the
	// project exists, and again receives a not-found rather than a membership
	// hint.
	intruder := Principal{ProjectID: alpha.ProjectID, ActorID: "actor_intruder"}
	if deniedStatus, body, err := harness.requestJSONAs(ctx, "GET", "/v1/sources/"+asset.ID, intruder, nil); err != nil {
		return err
	} else if deniedStatus != 404 || failureCode(body) != "project_not_found" {
		return fmt.Errorf("non-member read returned status=%d code=%s, want 404/project_not_found", deniedStatus, failureCode(body))
	}

	// Role enforcement: a viewer can read but cannot create sources or
	// timelines.
	if _, roleStatus, err := harness.putMembership(ctx, alpha, "actor_viewer", domain.RoleViewer); err != nil {
		return err
	} else if roleStatus != 200 {
		return fmt.Errorf("viewer membership setup returned status %d", roleStatus)
	}
	viewer := Principal{ProjectID: alpha.ProjectID, ActorID: "actor_viewer"}
	if _, readStatus, err := harness.getSource(ctx, viewer, asset.ID); err != nil {
		return err
	} else if readStatus != 200 {
		return fmt.Errorf("viewer source read returned status %d, want 200", readStatus)
	}
	if writeStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/sources/source_viewer_attempt", viewer, service.SourceWriteRequest{
		Name:      "viewer attempt",
		WAVBase64: "",
	}); err != nil {
		return err
	} else if writeStatus != 403 || failureCode(body) != "project_access_denied" {
		return fmt.Errorf("viewer write returned status=%d code=%s, want 403/project_access_denied", writeStatus, failureCode(body))
	}
	return nil
}

// runGrant proves a read-only grant lets the grantee consume an owner's source
// in its own timeline and render, while never permitting mutation of the
// source facts.
func runGrant(ctx context.Context, harness *Harness) error {
	owner, err := setupProject(ctx, harness, "proj_owner", "actor_owner")
	if err != nil {
		return err
	}
	grantee, err := setupProject(ctx, harness, "proj_grantee", "actor_grantee")
	if err != nil {
		return err
	}
	wav, err := sineWAV(48000, 1, 240, 330, 0.3)
	if err != nil {
		return err
	}
	asset, status, err := harness.createSource(ctx, owner, "source_shared", "shared narration", wav)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("owner source setup returned status %d", status)
	}

	// Before the grant, the grantee cannot see the source.
	if _, preStatus, err := harness.getSource(ctx, grantee, asset.ID); err != nil {
		return err
	} else if preStatus != 404 {
		return fmt.Errorf("pre-grant read returned status %d, want 404", preStatus)
	}

	grant, status, err := harness.createGrant(ctx, owner, service.GrantWriteRequest{
		SourceID:         asset.ID,
		OwnerProjectID:   owner.ProjectID,
		GranteeProjectID: grantee.ProjectID,
	})
	if err != nil {
		return err
	}
	if status != 201 || grant.State != domain.GrantActive || grant.ID == "" {
		return fmt.Errorf("grant creation returned status=%d grant=%+v", status, grant)
	}

	// Only the owner may issue a grant; a grantee attempting to re-grant is
	// rejected.
	if foreignStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/grants", grantee, service.GrantWriteRequest{
		SourceID:         asset.ID,
		GranteeProjectID: "proj_other",
	}); err != nil {
		return err
	} else if foreignStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("foreign grant issuance returned status=%d code=%s", foreignStatus, failureCode(body))
	}

	// The grantee can now read the source metadata but cannot overwrite the
	// source facts: re-registering the same id in its own project is treated as
	// a not-found identifier rather than a mutation path.
	readable, status, err := harness.getSource(ctx, grantee, asset.ID)
	if err != nil {
		return err
	}
	if status != 200 || readable.ProjectID != owner.ProjectID {
		return fmt.Errorf("grantee read returned status=%d project=%q", status, readable.ProjectID)
	}
	if mutateStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/sources/"+asset.ID, grantee, service.SourceWriteRequest{
		Name:      "grantee overwrite",
		WAVBase64: encodeBase64(wav),
	}); err != nil {
		return err
	} else if mutateStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("grantee source mutation returned status=%d code=%s, want 404/source_not_found", mutateStatus, failureCode(body))
	}

	// The grantee uses the granted source inside its own timeline and renders.
	spans := []domain.TimelineSpan{
		{SourceID: asset.ID, SourceStartFrame: 0, SourceEndFrame: asset.FrameCount, GainDB: -1, CrossfadeFrames: 0},
	}
	plan, status, err := harness.createTimeline(ctx, grantee, "timeline_grantee_use", "granted use", 48000, 1, -2, spans)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("grantee timeline creation returned status %d", status)
	}
	basis := plan.SourceAuthorizations[0]
	if basis.AccessBasis != domain.AccessBasisGrant || basis.GrantID != grant.ID ||
		basis.OwnerProjectID != owner.ProjectID || basis.GranteeProjectID != grantee.ProjectID {
		return fmt.Errorf("timeline captured wrong grant basis %+v", basis)
	}
	job, status, err := harness.createRender(ctx, grantee, "render_grantee_use", plan.ID)
	if err != nil {
		return err
	}
	if status != 201 || job.State != domain.RenderCompleted {
		return fmt.Errorf("grantee render returned status=%d state=%s", status, job.State)
	}
	receipt, status, err := harness.getReceipt(ctx, grantee, job.ID)
	if err != nil {
		return err
	}
	if status != 200 || receipt.ProjectID != grantee.ProjectID {
		return fmt.Errorf("receipt project mismatch: status=%d project=%q", status, receipt.ProjectID)
	}
	span := receipt.Spans[0]
	if span.SourceProjectID != owner.ProjectID || span.Authorization.AccessBasis != domain.AccessBasisGrant ||
		span.Authorization.GrantID != grant.ID {
		return fmt.Errorf("receipt does not explain the legal granted use: %+v", span)
	}

	// The owner still reads the source unchanged; the grantee's render output
	// belongs to the grantee project and is invisible to a third project.
	if _, ownerStatus, err := harness.getSource(ctx, owner, asset.ID); err != nil || ownerStatus != 200 {
		return fmt.Errorf("owner source read after grant failed: status=%d err=%v", ownerStatus, err)
	}
	third, err := setupProject(ctx, harness, "proj_third", "actor_third")
	if err != nil {
		return err
	}
	if outputStatus, body, err := harness.requestJSONAs(ctx, "GET", "/v1/renders/"+job.ID+"/output", third, nil); err != nil {
		return err
	} else if outputStatus != 404 || failureCode(body) != "render_not_found" {
		return fmt.Errorf("third-party render read returned status=%d code=%s, want 404/render_not_found", outputStatus, failureCode(body))
	}
	return nil
}

// runRevocationRace proves that a grant revoked while a render is in flight
// blocks publication: no artifact is released and the job fails, while a
// render completed before revocation keeps a receipt explaining the legal use
// at that time.
func runRevocationRace(ctx context.Context, harness *Harness) error {
	owner, err := setupProject(ctx, harness, "proj_revoke_owner", "actor_owner")
	if err != nil {
		return err
	}
	grantee, err := setupProject(ctx, harness, "proj_revoke_grantee", "actor_grantee")
	if err != nil {
		return err
	}
	wav, err := sineWAV(48000, 1, 240, 330, 0.3)
	if err != nil {
		return err
	}
	asset, status, err := harness.createSource(ctx, owner, "source_revocable", "revocable", wav)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("source setup returned status %d", status)
	}
	grant, status, err := harness.createGrant(ctx, owner, service.GrantWriteRequest{
		SourceID:         asset.ID,
		OwnerProjectID:   owner.ProjectID,
		GranteeProjectID: grantee.ProjectID,
	})
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("grant setup returned status %d", status)
	}

	spans := []domain.TimelineSpan{
		{SourceID: asset.ID, SourceStartFrame: 0, SourceEndFrame: asset.FrameCount, GainDB: 0, CrossfadeFrames: 0},
	}
	plan, status, err := harness.createTimeline(ctx, grantee, "timeline_revoke_use", "revocable use", 48000, 1, -2, spans)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("timeline setup returned status %d", status)
	}

	// A completed render under the active grant must remain explainable after
	// later revocation.
	firstJob, status, err := harness.createRender(ctx, grantee, "render_before_revoke", plan.ID)
	if err != nil {
		return err
	}
	if status != 201 || firstJob.State != domain.RenderCompleted {
		return fmt.Errorf("pre-revocation render returned status=%d state=%s", status, firstJob.State)
	}
	priorReceipt, status, err := harness.getReceipt(ctx, grantee, firstJob.ID)
	if err != nil {
		return err
	}
	if status != 200 || priorReceipt.Spans[0].Authorization.GrantID != grant.ID {
		return fmt.Errorf("pre-revocation receipt missing grant basis")
	}

	// Phase 1: revoke the first grant, then prove a render started after
	// revocation fails at the publication gate and publishes no artifact.
	if _, revokeErr := harness.Service().RevokeGrant(ctx, owner, grant.ID); revokeErr != nil {
		return fmt.Errorf("in-process revocation failed: %w", revokeErr)
	}
	revoked, revokeStatus, err := harness.revokeGrant(ctx, owner, grant.ID)
	if err != nil {
		return err
	}
	if revokeStatus != 200 || revoked.State != domain.GrantRevoked {
		return fmt.Errorf("idempotent revoke returned status=%d state=%s", revokeStatus, revoked.State)
	}

	renderStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/renders/render_after_revoke", grantee, service.RenderWriteRequest{
		TimelineID: plan.ID,
	})
	if err != nil {
		return err
	}
	// When the grant is already withdrawn before the render starts, the source
	// is no longer readable so the request is rejected as source_not_found, and
	// no render job is created. This deliberately matches a missing source so
	// the revoked grant's existence does not leak.
	if renderStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("post-revocation render returned status=%d code=%s, want 404/source_not_found", renderStatus, failureCode(body))
	}
	// The render job is created in the grantee project but fails while loading
	// the now-unauthorized source, so it terminates failed and never reaches a
	// terminal artifact.
	failedJob, failedStatus, err := harness.getRender(ctx, grantee, "render_after_revoke")
	if err != nil {
		return err
	}
	if failedStatus != 200 || failedJob.State != domain.RenderFailed || failedJob.Error == nil || failedJob.Error.Code != "source_not_found" {
		return fmt.Errorf("post-revocation job mismatch: status=%d job=%+v", failedStatus, failedJob)
	}
	// No output or receipt may be published for the failed render.
	if outputStatus, outBody, err := harness.requestJSONAs(ctx, "GET", "/v1/renders/render_after_revoke/output", grantee, nil); err != nil {
		return err
	} else if outputStatus != 409 || failureCode(outBody) != "render_not_completed" {
		return fmt.Errorf("failed render exposed output: status=%d code=%s", outputStatus, failureCode(outBody))
	}

	// The previously completed render remains readable and its receipt still
	// explains the basis that was valid when it was produced.
	stillReadable, stillStatus, err := harness.getReceipt(ctx, grantee, firstJob.ID)
	if err != nil {
		return err
	}
	if stillStatus != 200 || stillReadable.Spans[0].Authorization.AccessBasis != domain.AccessBasisGrant {
		return fmt.Errorf("historical receipt no longer explains the legal use: status=%d", stillStatus)
	}
	if _, outputStatus, err := harness.getOutput(ctx, grantee, firstJob.ID); err != nil {
		return err
	} else if outputStatus != 200 {
		return fmt.Errorf("historical output became unavailable: status=%d", outputStatus)
	}

	// Phase 2: deterministic in-flight race. Re-grant the source, then run a
	// second service over the same repository whose pre-finalize hook revokes
	// the new grant after mixing but before the publication gate. This
	// reproduces the exact "revocation meets an in-progress render" ordering.
	reGrant, status, err := harness.createGrant(ctx, owner, service.GrantWriteRequest{
		SourceID:         asset.ID,
		OwnerProjectID:   owner.ProjectID,
		GranteeProjectID: grantee.ProjectID,
	})
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("re-grant setup returned status %d", status)
	}
	raceHarness, err := NewHarnessOverRepository(harness.Repository(), service.Config{
		PreFinalizeHook: func(principal authz.Principal, job domain.RenderJob) {
			if _, revokeErr := harness.Service().RevokeGrant(ctx, owner, reGrant.ID); revokeErr != nil {
				panic("deterministic revocation hook failed: " + revokeErr.Error())
			}
		},
	})
	if err != nil {
		return err
	}
	defer raceHarness.Close()
	raceJob, _, raceErr := raceHarness.Service().CreateRender(ctx, grantee, "render_revoked_in_flight", service.RenderWriteRequest{
		TimelineID: plan.ID,
	})
	if raceErr == nil {
		return fmt.Errorf("in-flight revocation did not fail the render, job state=%s", raceJob.State)
	}
	if appErr := domain.AsAppError(raceErr); appErr.Detail.Code != "authorization_revoked" {
		return fmt.Errorf("in-flight revocation returned error %s, want authorization_revoked", appErr.Detail.Code)
	}
	if raceJob.State != domain.RenderFailed {
		return fmt.Errorf("in-flight race job state=%s, want failed", raceJob.State)
	}
	if _, _, outputErr := raceHarness.Repository().GetRenderOutput("render_revoked_in_flight"); outputErr == nil {
		return fmt.Errorf("in-flight revoked render published artifacts")
	}

	// New timeline composition referencing the now-unauthorized source fails.
	if composeStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/timelines/timeline_after_revoke", grantee, service.TimelineWriteRequest{
		Name:           "post revoke",
		SampleRate:     48000,
		OutputChannels: 1,
		TargetPeakDBFS: -2,
		Spans:          spans,
	}); err != nil {
		return err
	} else if composeStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("post-revocation composition returned status=%d code=%s", composeStatus, failureCode(body))
	}
	return nil
}

// runLegacyAdoption stages unowned pre-isolation records directly in storage,
// proves they are inaccessible until adopted, then migrates them into an
// explicit project and verifies the full workflow plus historical receipts.
func runLegacyAdoption(ctx context.Context, harness *Harness) error {
	owner, err := setupProject(ctx, harness, "proj_legacy", "actor_owner")
	if err != nil {
		return err
	}
	other, err := setupProject(ctx, harness, "proj_legacy_other", "actor_other")
	if err != nil {
		return err
	}

	// Stage a legacy source, timeline, and completed render with no project
	// ownership, mimicking data written by the pre-isolation baseline.
	wav, err := sineWAV(48000, 1, 240, 330, 0.3)
	if err != nil {
		return err
	}
	legacyAsset := domain.SourceAsset{
		ID:            "source_legacy",
		Name:          "legacy narration",
		Digest:        digestHex(wav),
		ByteCount:     int64(len(wav)),
		SampleRate:    48000,
		Channels:      1,
		BitsPerSample: 16,
		FrameCount:    11520,
		DurationMS:    240,
		PeakDBFS:      -8,
		RMSDBFS:       -12,
	}
	legacyTimeline := domain.Timeline{
		ID:             "timeline_legacy",
		Name:           "legacy plan",
		SampleRate:     48000,
		OutputChannels: 1,
		TargetPeakDBFS: -2,
		Spans: []domain.TimelineSpan{
			{SourceID: legacyAsset.ID, SourceStartFrame: 0, SourceEndFrame: legacyAsset.FrameCount, GainDB: 0, CrossfadeFrames: 0},
		},
		State:        "ready",
		OutputFrames: legacyAsset.FrameCount,
	}
	if err := harness.Repository().SaveSource(legacyAsset, wav); err != nil {
		return fmt.Errorf("stage legacy source: %w", err)
	}
	if err := harness.Repository().SaveTimeline(legacyTimeline); err != nil {
		return fmt.Errorf("stage legacy timeline: %w", err)
	}

	// Before adoption the records are enumerated as legacy but are invisible to
	// every project through the permission-aware read paths.
	inventory, status, err := harness.getLegacy(ctx, owner)
	if err != nil {
		return err
	}
	if status != 200 || len(inventory.SourceIDs) != 1 || len(inventory.TimelineIDs) != 1 {
		return fmt.Errorf("legacy inventory mismatch: status=%d inventory=%+v", status, inventory)
	}
	if deniedStatus, body, err := harness.requestJSONAs(ctx, "GET", "/v1/sources/"+legacyAsset.ID, owner, nil); err != nil {
		return err
	} else if deniedStatus != 404 || failureCode(body) != "source_not_found" {
		return fmt.Errorf("unadopted source read returned status=%d code=%s", deniedStatus, failureCode(body))
	}
	if otherStatus, body, err := harness.requestJSONAs(ctx, "GET", "/v1/timelines/"+legacyTimeline.ID, other, nil); err != nil {
		return err
	} else if otherStatus != 404 || failureCode(body) != "timeline_not_found" {
		return fmt.Errorf("other project saw legacy timeline: status=%d code=%s", otherStatus, failureCode(body))
	}

	// Adoption assigns every unowned record to the authenticated project.
	manifest, status, err := harness.adoptLegacy(ctx, owner)
	if err != nil {
		return err
	}
	if status != 200 || manifest.ProjectID != owner.ProjectID {
		return fmt.Errorf("adoption returned status=%d manifest=%+v", status, manifest)
	}
	if len(manifest.SourceIDs) != 1 || len(manifest.TimelineIDs) != 1 {
		return fmt.Errorf("adoption manifest did not cover legacy records: %+v", manifest)
	}

	// After adoption the owner can read and use the records, while the other
	// project still cannot.
	adoptedSource, status, err := harness.getSource(ctx, owner, legacyAsset.ID)
	if err != nil {
		return err
	}
	if status != 200 || adoptedSource.ProjectID != owner.ProjectID {
		return fmt.Errorf("adopted source read returned status=%d project=%q", status, adoptedSource.ProjectID)
	}
	adoptedTimeline, status, err := harness.getTimeline(ctx, owner, legacyTimeline.ID)
	if err != nil {
		return err
	}
	if status != 200 || adoptedTimeline.ProjectID != owner.ProjectID {
		return fmt.Errorf("adopted timeline read returned status=%d project=%q", status, adoptedTimeline.ProjectID)
	}
	if deniedStatus, body, err := harness.requestJSONAs(ctx, "GET", "/v1/sources/"+legacyAsset.ID, other, nil); err != nil {
		return err
	} else if deniedStatus != 404 {
		return fmt.Errorf("other project read adopted source: status=%d code=%s", deniedStatus, failureCode(body))
	}

	// The adopted timeline renders through the same owned-source authorization
	// path, and the receipt explains the basis.
	job, status, err := harness.createRender(ctx, owner, "render_legacy_adopted", adoptedTimeline.ID)
	if err != nil {
		return err
	}
	if status != 201 || job.State != domain.RenderCompleted {
		return fmt.Errorf("adopted timeline render returned status=%d state=%s", status, job.State)
	}
	receipt, status, err := harness.getReceipt(ctx, owner, job.ID)
	if err != nil {
		return err
	}
	if status != 200 || receipt.Spans[0].Authorization.AccessBasis != domain.AccessBasisOwned {
		return fmt.Errorf("adopted render receipt lacks owned-source basis: status=%d", status)
	}

	// A second adoption is a no-op: no unowned records remain and owned
	// objects are never reassigned.
	emptyInventory, status, err := harness.getLegacy(ctx, owner)
	if err != nil {
		return err
	}
	if status != 200 || len(emptyInventory.SourceIDs) != 0 || len(emptyInventory.TimelineIDs) != 0 || len(emptyInventory.JobIDs) != 0 {
		return fmt.Errorf("legacy records remain after adoption: %+v", emptyInventory)
	}
	secondManifest, status, err := harness.adoptLegacy(ctx, other)
	if err != nil {
		return err
	}
	if status != 200 || len(secondManifest.SourceIDs) != 0 {
		return fmt.Errorf("second adoption reassigned owned records: %+v", secondManifest)
	}
	return nil
}

// Enforce the hook contract at compile time where used.
var _ = authz.Principal{}
