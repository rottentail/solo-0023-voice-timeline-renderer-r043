package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
)

func (s *Service) CreateTimeline(ctx context.Context, principal authz.Principal, id string, request TimelineWriteRequest) (domain.Timeline, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Timeline{}, false, err
	}
	// Composition writes inside the caller's project and needs editor rights.
	if _, err := s.guard.RequireRole(principal, domain.RoleEditor); err != nil {
		return domain.Timeline{}, false, err
	}
	candidate := domain.Timeline{
		ID:             id,
		ProjectID:      principal.ProjectID,
		Name:           strings.TrimSpace(request.Name),
		SampleRate:     request.SampleRate,
		OutputChannels: request.OutputChannels,
		TargetPeakDBFS: request.TargetPeakDBFS,
		Spans:          append([]domain.TimelineSpan(nil), request.Spans...),
		State:          "validating",
	}
	existing, getErr := s.repo.GetTimeline(id)
	if getErr == nil {
		if existing.ProjectID != principal.ProjectID {
			return domain.Timeline{}, false, domain.E(domain.KindNotFound, "timeline_not_found", "timeline_id", "timeline does not exist or is not accessible to the project")
		}
		if equivalentTimeline(existing, candidate) {
			return existing, false, nil
		}
		return domain.Timeline{}, false, domain.E(domain.KindConflict, "timeline_conflict", "timeline_id", "identifier is already bound to a different timeline")
	}
	if appErr := domain.AsAppError(getErr); appErr.Kind != domain.KindNotFound {
		return domain.Timeline{}, false, getErr
	}

	sources := make(map[string]domain.SourceAsset)
	authorizations := make(map[string]domain.SourceAuthorization)
	for index, span := range candidate.Spans {
		field := fmt.Sprintf("spans[%d].source_id", index)
		if _, resolved := sources[span.SourceID]; resolved {
			continue
		}
		asset, err := s.repo.GetSourceMeta(span.SourceID)
		if err != nil {
			// Resolve every cross-project source through the same guard rather
			// than trusting storage existence alone.
			if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
				return domain.Timeline{}, false, domain.E(domain.KindNotFound, "source_not_found", field, "source does not exist or is not accessible to the project")
			}
			return domain.Timeline{}, false, err
		}
		authorization, err := s.guard.AuthorizeSourceUse(principal, asset)
		if err != nil {
			if appErr := domain.AsAppError(err); appErr.Kind == domain.KindNotFound {
				return domain.Timeline{}, false, domain.E(domain.KindNotFound, "source_not_found", field, "source does not exist or is not accessible to the project")
			}
			return domain.Timeline{}, false, err
		}
		sources[span.SourceID] = asset
		authorizations[span.SourceID] = authorization
	}
	if err := domain.ValidateTimeline(candidate, sources, s.config.MaxSpans, s.config.MaxOutputFrames); err != nil {
		return domain.Timeline{}, false, err
	}
	candidate.SourceAuthorizations = make([]domain.SourceAuthorization, 0, len(authorizations))
	for _, span := range candidate.Spans {
		if basis, ok := authorizations[span.SourceID]; ok {
			candidate.SourceAuthorizations = append(candidate.SourceAuthorizations, basis)
			delete(authorizations, span.SourceID)
		}
	}
	candidate.OutputFrames = domain.TimelineOutputFrames(candidate, sources)
	candidate.State = "ready"
	candidate.CreatedAt = time.Now().UTC()
	if err := s.repo.SaveTimeline(candidate); err != nil {
		return domain.Timeline{}, false, err
	}
	return candidate, true, nil
}

func equivalentTimeline(left, right domain.Timeline) bool {
	if left.Name != right.Name ||
		left.SampleRate != right.SampleRate ||
		left.OutputChannels != right.OutputChannels ||
		math.Abs(left.TargetPeakDBFS-right.TargetPeakDBFS) > 1e-9 ||
		len(left.Spans) != len(right.Spans) {
		return false
	}
	for index := range left.Spans {
		a := left.Spans[index]
		b := right.Spans[index]
		if a.SourceID != b.SourceID ||
			a.SourceStartFrame != b.SourceStartFrame ||
			a.SourceEndFrame != b.SourceEndFrame ||
			math.Abs(a.GainDB-b.GainDB) > 1e-9 ||
			a.CrossfadeFrames != b.CrossfadeFrames {
			return false
		}
	}
	return true
}
