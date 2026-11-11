package service

import (
	"context"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/render"
)

func (s *Service) CreateRender(
	ctx context.Context,
	principal authz.Principal,
	id string,
	request RenderWriteRequest,
) (domain.RenderJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.RenderJob{}, false, err
	}
	if _, err := s.guard.RequireRole(principal, domain.RoleEditor); err != nil {
		return domain.RenderJob{}, false, err
	}
	if err := domain.ValidateResourceID("render_id", id); err != nil {
		return domain.RenderJob{}, false, err
	}
	if err := domain.ValidateResourceID("timeline_id", request.TimelineID); err != nil {
		return domain.RenderJob{}, false, err
	}

	existing, getErr := s.repo.GetJob(id)
	if getErr == nil {
		if existing.ProjectID != principal.ProjectID {
			return domain.RenderJob{}, false, domain.E(domain.KindNotFound, "render_not_found", "render_id", "render does not exist or is not accessible to the project")
		}
		if existing.TimelineID != request.TimelineID {
			return domain.RenderJob{}, false, domain.E(domain.KindConflict, "render_conflict", "render_id", "identifier is already bound to a different timeline")
		}
		return existing, false, nil
	}
	if appErr := domain.AsAppError(getErr); appErr.Kind != domain.KindNotFound {
		return domain.RenderJob{}, false, getErr
	}

	plan, err := s.repo.GetTimeline(request.TimelineID)
	if err != nil {
		return domain.RenderJob{}, false, err
	}
	if plan.ProjectID != principal.ProjectID {
		return domain.RenderJob{}, false, domain.E(domain.KindNotFound, "timeline_not_found", "timeline_id", "timeline does not exist or is not accessible to the project")
	}
	now := time.Now().UTC()
	job := domain.RenderJob{
		ID:         id,
		ProjectID:  principal.ProjectID,
		TimelineID: plan.ID,
		State:      domain.RenderQueued,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.repo.SaveJob(job); err != nil {
		return domain.RenderJob{}, false, err
	}
	if err := s.advanceJob(&job, domain.RenderRendering, nil); err != nil {
		return job, true, err
	}

	// Re-resolve every source through the guard at render time. The
	// authorization bases captured here are the facts re-verified before the
	// artifacts are published, so a revocation lands before publication.
	sourceInputs := make(map[string]render.SourceInput)
	authorizations := make([]domain.SourceAuthorization, 0, len(plan.SourceAuthorizations))
	for _, span := range plan.Spans {
		if _, loaded := sourceInputs[span.SourceID]; loaded {
			continue
		}
		asset, payload, loadErr := s.repo.GetSource(span.SourceID)
		if loadErr != nil {
			return s.failRender(job, loadErr)
		}
		authorization, authErr := s.guard.AuthorizeSourceRead(principal, asset)
		if authErr != nil {
			return s.failRender(job, authErr)
		}
		sourceInputs[span.SourceID] = render.SourceInput{
			Asset:         asset,
			WAV:           payload,
			Authorization: authorization,
		}
		authorizations = append(authorizations, authorization)
	}
	result, renderErr := render.Render(
		ctx,
		id,
		plan,
		sourceInputs,
		render.Limits{MaxOutputFrames: s.config.MaxOutputFrames},
		principal.ProjectID,
		authorizations,
		time.Now().UTC(),
	)
	if renderErr != nil {
		return s.failRender(job, domain.Wrap(domain.KindInternal, "render_failed", "", "timeline rendering failed", renderErr))
	}
	if err := s.advanceJob(&job, domain.RenderFinalizing, nil); err != nil {
		return job, true, err
	}

	// Publication gate: no artifact may be committed on an authorization basis
	// that was withdrawn before this point. The optional hook lets revocation
	// races be tested deterministically; the verification itself always runs.
	if s.config.PreFinalizeHook != nil {
		s.config.PreFinalizeHook(principal, job)
	}
	if err := s.guard.VerifyPublication(principal, authorizations); err != nil {
		return s.failRender(job, err)
	}
	if err := s.repo.CommitRenderArtifacts(id, result.WAV, result.Receipt); err != nil {
		return s.failRender(job, err)
	}
	job.Output = &result.Output
	job.ReceiptDigest = result.Output.Digest
	if err := s.advanceJob(&job, domain.RenderCompleted, nil); err != nil {
		return job, true, err
	}
	return job, true, nil
}

func (s *Service) advanceJob(job *domain.RenderJob, state domain.RenderState, detail *domain.ErrorDetail) error {
	if err := job.Transition(state, detail, time.Now().UTC()); err != nil {
		return err
	}
	if err := s.repo.SaveJob(*job); err != nil {
		return err
	}
	return nil
}

func (s *Service) failRender(job domain.RenderJob, cause error) (domain.RenderJob, bool, error) {
	appErr := domain.AsAppError(cause)
	detail := &domain.ErrorDetail{
		Code:    appErr.Detail.Code,
		Message: appErr.Detail.Message,
		Field:   appErr.Detail.Field,
	}
	transitionErr := job.Transition(domain.RenderFailed, detail, time.Now().UTC())
	if transitionErr != nil {
		return job, true, transitionErr
	}
	job.Output = nil
	job.ReceiptDigest = ""
	if saveErr := s.repo.SaveJob(job); saveErr != nil {
		return job, true, saveErr
	}
	return job, true, appErr
}
