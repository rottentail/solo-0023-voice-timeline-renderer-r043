package checkflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/service"
)

func Run(ctx context.Context, workflow string) error {
	harness, err := NewHarness()
	if err != nil {
		return err
	}
	defer harness.Close()

	switch workflow {
	case "ingest-audio-source":
		return runIngest(ctx, harness)
	case "compose-timeline":
		return runCompose(ctx, harness)
	case "render-mixdown":
		return runRender(ctx, harness)
	case "project-isolation":
		return runIsolation(ctx, harness)
	case "cross-project-grant":
		return runGrant(ctx, harness)
	case "grant-revocation-race":
		return runRevocationRace(ctx, harness)
	case "legacy-adoption":
		return runLegacyAdoption(ctx, harness)
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

// setupProject provisions a project with an owner actor and returns the owner
// principal used by the workflow.
func setupProject(ctx context.Context, harness *Harness, projectID, actorID string) (Principal, error) {
	principal := Principal{ProjectID: projectID, ActorID: actorID}
	// Provisioning authenticates the platform actor but assigns ownerID as the
	// project's founding owner.
	if _, status, err := harness.createProject(ctx, principal, projectID, projectID+" workspace", actorID); err != nil {
		return Principal{}, fmt.Errorf("project setup failed: %w", err)
	} else if status != 201 {
		return Principal{}, fmt.Errorf("project setup returned status %d, want 201", status)
	}
	return principal, nil
}

func runIngest(ctx context.Context, harness *Harness) error {
	principal, err := setupProject(ctx, harness, "proj_ingest", "actor_owner")
	if err != nil {
		return err
	}
	wav, err := sineWAV(48000, 1, 250, 440, 0.4)
	if err != nil {
		return err
	}
	asset, status, err := harness.createSource(ctx, principal, "source_ingest_01", "narration take", wav)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("source write returned status %d, want 201", status)
	}
	if asset.ProjectID != principal.ProjectID {
		return fmt.Errorf("source ownership missing: project_id=%q", asset.ProjectID)
	}
	if asset.FrameCount != 12000 || asset.BitsPerSample != 16 {
		return fmt.Errorf("source metadata is inconsistent: %+v", asset)
	}
	readBack, status, err := harness.getSource(ctx, principal, asset.ID)
	if err != nil {
		return err
	}
	if status != 200 || readBack.Digest != asset.Digest {
		return fmt.Errorf("source read-back mismatch: status=%d digest=%s", status, readBack.Digest)
	}
	badStatus, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/sources/source_ingest_bad", principal, service.SourceWriteRequest{
		Name:      "broken take",
		WAVBase64: base64.StdEncoding.EncodeToString([]byte("not a wave")),
	})
	if err != nil {
		return err
	}
	if badStatus != 422 || failureCode(body) != "invalid_wav" {
		return fmt.Errorf("malformed media returned status=%d code=%s", badStatus, failureCode(body))
	}
	missing, status, err := harness.getSource(ctx, principal, "source_ingest_bad")
	if err != nil {
		return err
	}
	if status != 404 || missing.ID != "" {
		return fmt.Errorf("failed source became visible: status=%d id=%s", status, missing.ID)
	}

	// An unauthenticated caller must be rejected rather than reaching storage.
	unauthStatus, _, err := harness.requestJSONAs(ctx, "GET", "/v1/sources/"+asset.ID, Principal{}, nil)
	if err != nil {
		return err
	}
	if unauthStatus != 401 {
		return fmt.Errorf("unauthenticated source read returned status %d, want 401", unauthStatus)
	}
	return nil
}

func runCompose(ctx context.Context, harness *Harness) error {
	principal, err := setupProject(ctx, harness, "proj_compose", "actor_owner")
	if err != nil {
		return err
	}
	first, err := sineWAV(48000, 1, 300, 220, 0.35)
	if err != nil {
		return err
	}
	second, err := sineWAV(44100, 2, 250, 330, 0.25)
	if err != nil {
		return err
	}
	if _, status, err := harness.createSource(ctx, principal, "source_compose_a", "first span", first); err != nil || status != 201 {
		return fmt.Errorf("first source setup failed: status=%d err=%v", status, err)
	}
	if _, status, err := harness.createSource(ctx, principal, "source_compose_b", "second span", second); err != nil || status != 201 {
		return fmt.Errorf("second source setup failed: status=%d err=%v", status, err)
	}
	validSpans := []domain.TimelineSpan{
		{SourceID: "source_compose_a", SourceStartFrame: 0, SourceEndFrame: 14400, GainDB: -3, CrossfadeFrames: 0},
		{SourceID: "source_compose_b", SourceStartFrame: 0, SourceEndFrame: 11025, GainDB: 1, CrossfadeFrames: 2400},
	}
	plan, status, err := harness.createTimeline(ctx, principal, "timeline_compose_01", "two span assembly", 48000, 2, -1, validSpans)
	if err != nil {
		return err
	}
	if status != 201 || plan.State != "ready" {
		return fmt.Errorf("timeline write returned status=%d state=%s", status, plan.State)
	}
	if len(plan.SourceAuthorizations) != 2 {
		return fmt.Errorf("timeline did not capture %d authorizations, got %d", 2, len(plan.SourceAuthorizations))
	}
	for _, basis := range plan.SourceAuthorizations {
		if basis.AccessBasis != domain.AccessBasisOwned || basis.OwnerProjectID != principal.ProjectID {
			return fmt.Errorf("timeline captured unexpected authorization %+v", basis)
		}
	}
	readBack, status, err := harness.getTimeline(ctx, principal, plan.ID)
	if err != nil {
		return err
	}
	if status != 200 || readBack.OutputFrames != plan.OutputFrames || len(readBack.Spans) != 2 {
		return fmt.Errorf("timeline read-back mismatch: status=%d frames=%d", status, readBack.OutputFrames)
	}
	invalidSpans := append([]domain.TimelineSpan(nil), validSpans...)
	invalidSpans[1].CrossfadeFrames = 999999
	_, status, err = harness.createTimeline(ctx, principal, "timeline_compose_bad", "invalid crossfade", 48000, 2, -1, invalidSpans)
	if err != nil {
		return err
	}
	if status != 422 {
		return fmt.Errorf("invalid crossfade returned status %d, want 422", status)
	}
	return nil
}

func runRender(ctx context.Context, harness *Harness) error {
	principal, err := setupProject(ctx, harness, "proj_render", "actor_owner")
	if err != nil {
		return err
	}
	first, err := sineWAV(48000, 1, 400, 220, 0.25)
	if err != nil {
		return err
	}
	second, err := sineWAV(44100, 2, 350, 330, 0.2)
	if err != nil {
		return err
	}
	if _, status, err := harness.createSource(ctx, principal, "source_render_a", "opening take", first); err != nil || status != 201 {
		return fmt.Errorf("opening source setup failed: status=%d err=%v", status, err)
	}
	if _, status, err := harness.createSource(ctx, principal, "source_render_b", "closing take", second); err != nil || status != 201 {
		return fmt.Errorf("closing source setup failed: status=%d err=%v", status, err)
	}
	spans := []domain.TimelineSpan{
		{SourceID: "source_render_a", SourceStartFrame: 0, SourceEndFrame: 19200, GainDB: -2, CrossfadeFrames: 0},
		{SourceID: "source_render_b", SourceStartFrame: 0, SourceEndFrame: 15435, GainDB: 0, CrossfadeFrames: 4800},
	}
	plan, status, err := harness.createTimeline(ctx, principal, "timeline_render_01", "crossfaded narration", 48000, 2, -1, spans)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("timeline setup returned status %d", status)
	}
	job, status, err := harness.createRender(ctx, principal, "render_mixdown_01", plan.ID)
	if err != nil {
		return err
	}
	if status != 201 || job.State != domain.RenderCompleted || job.Output == nil {
		return fmt.Errorf("render returned status=%d state=%s output=%v", status, job.State, job.Output)
	}
	fetched, status, err := harness.getRender(ctx, principal, job.ID)
	if err != nil {
		return err
	}
	if status != 200 || fetched.State != domain.RenderCompleted {
		return fmt.Errorf("render read-back returned status=%d state=%s", status, fetched.State)
	}
	receipt, status, err := harness.getReceipt(ctx, principal, job.ID)
	if err != nil {
		return err
	}
	if status != 200 || receipt.Output.FrameCount != plan.OutputFrames {
		return fmt.Errorf("receipt frame mismatch: status=%d receipt=%d plan=%d", status, receipt.Output.FrameCount, plan.OutputFrames)
	}
	if receipt.ProjectID != principal.ProjectID || len(receipt.SourceAuthorizations) != 2 {
		return fmt.Errorf("receipt authorization section missing: project=%q bases=%d", receipt.ProjectID, len(receipt.SourceAuthorizations))
	}
	for _, span := range receipt.Spans {
		if span.SourceProjectID != principal.ProjectID || span.Authorization.AccessBasis != domain.AccessBasisOwned {
			return fmt.Errorf("receipt span lacks authorization explanation: %+v", span)
		}
	}
	audioBytes, status, err := harness.getOutput(ctx, principal, job.ID)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("output returned status %d", status)
	}
	digest := sha256.Sum256(audioBytes)
	if hex.EncodeToString(digest[:]) != receipt.Output.Digest {
		return fmt.Errorf("output digest does not match receipt")
	}
	clip, info, err := audiocodec.DecodeWAV(audioBytes)
	if err != nil {
		return fmt.Errorf("decode rendered output: %w", err)
	}
	measured := audiocodec.Measure(clip.Samples)
	if info.FrameCount != receipt.Output.FrameCount {
		return fmt.Errorf("decoded frame count %d != receipt %d", info.FrameCount, receipt.Output.FrameCount)
	}
	if math.Abs(measured.PeakDBFS-(-1)) > 0.25 {
		return fmt.Errorf("output peak %.3f dBFS does not meet -1 dBFS target", measured.PeakDBFS)
	}
	return nil
}
