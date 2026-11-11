package checkflow

import (
	"context"
	"encoding/base64"
	"fmt"

	"voice-timeline-renderer/internal/domain"
	"voice-timeline-renderer/internal/service"
)

type failureEnvelope struct {
	Error domain.ErrorDetail `json:"error"`
	Job   *domain.RenderJob  `json:"job,omitempty"`
}

func (harness *Harness) createProject(
	ctx context.Context,
	principal Principal,
	projectID, name, ownerID string,
) (domain.Project, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/projects/"+projectID, principal, service.ProjectWriteRequest{
		Name:    name,
		OwnerID: ownerID,
	})
	if err != nil {
		return domain.Project{}, status, err
	}
	var project domain.Project
	if status == 200 || status == 201 {
		if err := decodeBody(body, &project); err != nil {
			return domain.Project{}, status, err
		}
	}
	return project, status, nil
}

func (harness *Harness) putMembership(
	ctx context.Context,
	principal Principal,
	actorID string,
	role domain.Role,
) (domain.Membership, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/memberships", principal, service.MembershipWriteRequest{
		ActorID: actorID,
		Role:    string(role),
	})
	if err != nil {
		return domain.Membership{}, status, err
	}
	var membership domain.Membership
	if status == 200 {
		if err := decodeBody(body, &membership); err != nil {
			return domain.Membership{}, status, err
		}
	}
	return membership, status, nil
}

func (harness *Harness) createSource(
	ctx context.Context,
	principal Principal,
	id string,
	name string,
	wav []byte,
) (domain.SourceAsset, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/sources/"+id, principal, service.SourceWriteRequest{
		Name:      name,
		WAVBase64: base64.StdEncoding.EncodeToString(wav),
	})
	if err != nil {
		return domain.SourceAsset{}, status, err
	}
	var asset domain.SourceAsset
	if status == 200 || status == 201 {
		if err := decodeBody(body, &asset); err != nil {
			return domain.SourceAsset{}, status, err
		}
	}
	return asset, status, nil
}

func (harness *Harness) getSource(
	ctx context.Context,
	principal Principal,
	id string,
) (domain.SourceAsset, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/sources/"+id, principal, nil)
	if err != nil {
		return domain.SourceAsset{}, status, err
	}
	var asset domain.SourceAsset
	if status == 200 {
		if err := decodeBody(body, &asset); err != nil {
			return domain.SourceAsset{}, status, err
		}
	}
	return asset, status, nil
}

func (harness *Harness) createTimeline(
	ctx context.Context,
	principal Principal,
	id string,
	name string,
	sampleRate int,
	channels int,
	targetPeak float64,
	spans []domain.TimelineSpan,
) (domain.Timeline, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/timelines/"+id, principal, service.TimelineWriteRequest{
		Name:           name,
		SampleRate:     sampleRate,
		OutputChannels: channels,
		TargetPeakDBFS: targetPeak,
		Spans:          spans,
	})
	if err != nil {
		return domain.Timeline{}, status, err
	}
	var plan domain.Timeline
	if status == 200 || status == 201 {
		if err := decodeBody(body, &plan); err != nil {
			return domain.Timeline{}, status, err
		}
	}
	return plan, status, nil
}

func (harness *Harness) getTimeline(
	ctx context.Context,
	principal Principal,
	id string,
) (domain.Timeline, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/timelines/"+id, principal, nil)
	if err != nil {
		return domain.Timeline{}, status, err
	}
	var plan domain.Timeline
	if status == 200 {
		if err := decodeBody(body, &plan); err != nil {
			return domain.Timeline{}, status, err
		}
	}
	return plan, status, nil
}

func (harness *Harness) createRender(
	ctx context.Context,
	principal Principal,
	id string,
	timelineID string,
) (domain.RenderJob, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/renders/"+id, principal, service.RenderWriteRequest{
		TimelineID: timelineID,
	})
	if err != nil {
		return domain.RenderJob{}, status, err
	}
	var job domain.RenderJob
	if status == 200 || status == 201 {
		if err := decodeBody(body, &job); err != nil {
			return domain.RenderJob{}, status, err
		}
	}
	return job, status, nil
}

func (harness *Harness) getRender(
	ctx context.Context,
	principal Principal,
	id string,
) (domain.RenderJob, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/renders/"+id, principal, nil)
	if err != nil {
		return domain.RenderJob{}, status, err
	}
	var job domain.RenderJob
	if status == 200 {
		if err := decodeBody(body, &job); err != nil {
			return domain.RenderJob{}, status, err
		}
	}
	return job, status, nil
}

func (harness *Harness) getReceipt(
	ctx context.Context,
	principal Principal,
	id string,
) (domain.RenderReceipt, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/renders/"+id+"/receipt", principal, nil)
	if err != nil {
		return domain.RenderReceipt{}, status, err
	}
	var receipt domain.RenderReceipt
	if status == 200 {
		if err := decodeBody(body, &receipt); err != nil {
			return domain.RenderReceipt{}, status, err
		}
	}
	return receipt, status, nil
}

func (harness *Harness) getOutput(
	ctx context.Context,
	principal Principal,
	id string,
) ([]byte, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/renders/"+id+"/output", principal, nil)
	if err != nil {
		return nil, status, err
	}
	if status != 200 {
		var envelope failureEnvelope
		_ = decodeBody(body, &envelope)
		return nil, status, fmt.Errorf("unexpected output status %d with error %s", status, envelope.Error.Code)
	}
	return body, status, nil
}

func (harness *Harness) createGrant(
	ctx context.Context,
	principal Principal,
	request service.GrantWriteRequest,
) (domain.Grant, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "PUT", "/v1/grants", principal, request)
	if err != nil {
		return domain.Grant{}, status, err
	}
	var grant domain.Grant
	if status == 201 {
		if err := decodeBody(body, &grant); err != nil {
			return domain.Grant{}, status, err
		}
	}
	return grant, status, nil
}

func (harness *Harness) revokeGrant(
	ctx context.Context,
	principal Principal,
	grantID string,
) (domain.Grant, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "POST", "/v1/grants/"+grantID+"/revoke", principal, nil)
	if err != nil {
		return domain.Grant{}, status, err
	}
	var grant domain.Grant
	if status == 200 {
		if err := decodeBody(body, &grant); err != nil {
			return domain.Grant{}, status, err
		}
	}
	return grant, status, nil
}

func (harness *Harness) getLegacy(ctx context.Context, principal Principal) (domain.LegacyInventory, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "GET", "/v1/legacy", principal, nil)
	if err != nil {
		return domain.LegacyInventory{}, status, err
	}
	var inventory domain.LegacyInventory
	if status == 200 {
		if err := decodeBody(body, &inventory); err != nil {
			return domain.LegacyInventory{}, status, err
		}
	}
	return inventory, status, nil
}

func (harness *Harness) adoptLegacy(ctx context.Context, principal Principal) (domain.AdoptionManifest, int, error) {
	status, body, err := harness.requestJSONAs(ctx, "POST", "/v1/legacy/adopt", principal, nil)
	if err != nil {
		return domain.AdoptionManifest{}, status, err
	}
	var manifest domain.AdoptionManifest
	if status == 200 {
		if err := decodeBody(body, &manifest); err != nil {
			return domain.AdoptionManifest{}, status, err
		}
	}
	return manifest, status, nil
}

func failureCode(body []byte) string {
	var envelope failureEnvelope
	if err := decodeBody(body, &envelope); err != nil {
		return ""
	}
	return envelope.Error.Code
}
