package service

import (
	"context"
	"sync"
	"time"

	"voice-timeline-renderer/internal/authz"
	"voice-timeline-renderer/internal/domain"
)

type Repository interface {
	// Project and membership storage.
	CreateProject(project domain.Project, ownerID string, ownerRole domain.Role, now time.Time) error
	GetProject(string) (domain.Project, error)
	ListProjects() ([]domain.Project, error)
	GetMembership(projectID, actorID string) (domain.Membership, error)
	PutMembership(membership domain.Membership) error

	// Grant storage.
	CreateGrant(domain.Grant) error
	GetGrant(string) (domain.Grant, error)
	RevokeGrant(string, time.Time) (domain.Grant, error)
	FindActiveGrant(sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error)
	GrantIsActive(grantID, sourceID, ownerProjectID, granteeProjectID string) (domain.Grant, error)
	ListGrantsForProject(projectID string) ([]domain.Grant, error)

	// Asset storage.
	SaveSource(domain.SourceAsset, []byte) error
	GetSourceMeta(string) (domain.SourceAsset, error)
	GetSource(string) (domain.SourceAsset, []byte, error)
	SaveTimeline(domain.Timeline) error
	GetTimeline(string) (domain.Timeline, error)
	SaveJob(domain.RenderJob) error
	GetJob(string) (domain.RenderJob, error)
	CommitRenderArtifacts(string, []byte, domain.RenderReceipt) error
	GetRenderOutput(string) ([]byte, domain.RenderReceipt, error)

	// Legacy ownership migration.
	ListLegacyRecords() (domain.LegacyInventory, error)
	AdoptLegacyRecords(projectID string, now time.Time) (domain.AdoptionManifest, error)
}

type Config struct {
	MaxSourceBytes  int64
	MaxSpans        int
	MaxOutputFrames int64
	// PreFinalizeHook runs after mixing has finished but before a render
	// publishes its artifacts, while holding the service lock. It exists so
	// revocation races can be exercised deterministically; production leaves it
	// nil. The publication-time grant verification always runs regardless.
	PreFinalizeHook func(principal authz.Principal, job domain.RenderJob)
}

func DefaultConfig() Config {
	return Config{
		MaxSourceBytes:  32 << 20,
		MaxSpans:        64,
		MaxOutputFrames: 48000 * 180,
	}
}

type Service struct {
	repo   Repository
	guard  *authz.Guard
	config Config
	mu     sync.Mutex
}

func New(repo Repository, guard *authz.Guard, config Config) *Service {
	defaults := DefaultConfig()
	if config.MaxSourceBytes <= 0 {
		config.MaxSourceBytes = defaults.MaxSourceBytes
	}
	if config.MaxSpans <= 0 {
		config.MaxSpans = defaults.MaxSpans
	}
	if config.MaxOutputFrames <= 0 {
		config.MaxOutputFrames = defaults.MaxOutputFrames
	}
	return &Service{repo: repo, guard: guard, config: config}
}

type SourceWriteRequest struct {
	Name      string `json:"name"`
	WAVBase64 string `json:"wav_base64"`
}

type TimelineWriteRequest struct {
	Name           string                `json:"name"`
	SampleRate     int                   `json:"sample_rate"`
	OutputChannels int                   `json:"output_channels"`
	TargetPeakDBFS float64               `json:"target_peak_dbfs"`
	Spans          []domain.TimelineSpan `json:"spans"`
}

type RenderWriteRequest struct {
	TimelineID string `json:"timeline_id"`
}

type ProjectWriteRequest struct {
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id"`
	OwnerRole string `json:"owner_role,omitempty"`
}

type MembershipWriteRequest struct {
	ActorID string `json:"actor_id"`
	Role    string `json:"role"`
}

type GrantWriteRequest struct {
	SourceID         string `json:"source_id"`
	OwnerProjectID   string `json:"owner_project_id"`
	GranteeProjectID string `json:"grantee_project_id"`
}

type RenderResult struct {
	Job domain.RenderJob
	WAV []byte
}

func (s *Service) GetSource(ctx context.Context, principal authz.Principal, id string) (domain.SourceAsset, error) {
	if err := ctx.Err(); err != nil {
		return domain.SourceAsset{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.repo.GetSourceMeta(id)
	if err != nil {
		return domain.SourceAsset{}, err
	}
	if _, err := s.guard.AuthorizeSourceRead(principal, asset); err != nil {
		return domain.SourceAsset{}, err
	}
	return asset, nil
}

func (s *Service) GetTimeline(ctx context.Context, principal authz.Principal, id string) (domain.Timeline, error) {
	if err := ctx.Err(); err != nil {
		return domain.Timeline{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := s.repo.GetTimeline(id)
	if err != nil {
		return domain.Timeline{}, err
	}
	if err := authz.AuthorizeResourceRead(principal, plan.ProjectID, id, "timeline_not_found", "timeline_id", "timeline does not exist or is not accessible to the project"); err != nil {
		return domain.Timeline{}, err
	}
	return plan, nil
}

func (s *Service) GetRender(ctx context.Context, principal authz.Principal, id string) (domain.RenderJob, error) {
	if err := ctx.Err(); err != nil {
		return domain.RenderJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.repo.GetJob(id)
	if err != nil {
		return domain.RenderJob{}, err
	}
	if err := authz.AuthorizeResourceRead(principal, job.ProjectID, id, "render_not_found", "render_id", "render does not exist or is not accessible to the project"); err != nil {
		return domain.RenderJob{}, err
	}
	return job, nil
}

func (s *Service) GetReceipt(ctx context.Context, principal authz.Principal, id string) (domain.RenderReceipt, error) {
	job, err := s.GetRender(ctx, principal, id)
	if err != nil {
		return domain.RenderReceipt{}, err
	}
	if job.State != domain.RenderCompleted {
		return domain.RenderReceipt{}, domain.E(domain.KindUnready, "render_not_completed", "render_id", "render receipt is available only after completion")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, receipt, err := s.repo.GetRenderOutput(id)
	return receipt, err
}

func (s *Service) GetOutput(ctx context.Context, principal authz.Principal, id string) ([]byte, domain.RenderReceipt, error) {
	job, err := s.GetRender(ctx, principal, id)
	if err != nil {
		return nil, domain.RenderReceipt{}, err
	}
	if job.State != domain.RenderCompleted {
		return nil, domain.RenderReceipt{}, domain.E(domain.KindUnready, "render_not_completed", "render_id", "render output is available only after completion")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repo.GetRenderOutput(id)
}
