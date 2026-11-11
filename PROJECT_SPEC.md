# Voice Timeline Renderer

## Service goal

Voice Timeline Renderer is a backend service for assembling narrated PCM audio
into a deterministic mixdown. It gives audio-platform engineers a small HTTP
surface for registering 16-bit WAV sources, composing ordered timeline spans,
and rendering a normalized output along with a machine-readable render
receipt.

The project is intentionally production-only. Automated tests, fixtures, and a
test command are deferred to the downstream task stage, which will add focused
unit, integration, and end-to-end verification.

## Users

- Audio-platform engineers who need a self-hosted assembly stage.
- Audio editing tooling that submits WAV sources and timeline operations.
- Quality engineers who replay a render and compare the receipt with the output.

## Project shape and stack

- Shape: backend.
- Language: Go.
- Runtime: Go bundled HTTP packages.
- Persistence: a local data directory containing metadata JSON and media files.
- External services: none.

## Core entities

### Project and membership

A project is the ownership and isolation boundary. Every source, timeline, and
render job belongs to exactly one project. A membership binds an actor to a
project with one role: `owner`, `editor`, or `viewer`. Higher roles include
every lower role's permissions.

Invariants:

- Only members can authenticate into a project; a non-member receives the same
  not-found response as a request against a missing project.
- Editors and above create sources, timelines, and renders; owners manage
  memberships and grants; viewers have read-only access.
- Projects are isolated by default: no object of one project is visible to
  another project without an explicit grant.

### Cross-project source grant

A grant is issued by the project that owns a source and names one grantee
project. It conveys read-only use only.

Invariants:

- The grantee may read the source and use it in its own timelines and renders,
  but can never change the source or its measured facts.
- At most one active grant exists per `(source, owner, grantee)` triple;
  revocation is terminal, after which a new grant may be issued.
- A grant that is revoked is enforced before any render artifact is published.
- Denied or revoked access returns the same not-found shape as a missing
  source, so cross-project object existence never leaks.

### Source asset

A source asset is an immutable uploaded PCM WAV. Its identity, SHA-256 digest,
frame count, sample rate, channel count, peak amplitude, RMS amplitude, and duration are
recorded after decoding. A source is never updated in place.

Invariants:

- Every source carries the `project_id` of its owning project.
- The payload must be a RIFF/WAVE file with 16-bit integer PCM samples.
- Only mono and stereo layouts are accepted.
- The digest and signal measurements describe the exact bytes stored.
- Decoded payloads are bounded by configured byte and duration limits.
- A non-owning project may read the source only through an active grant and can
  never mutate it.

### Assembly timeline

A timeline is an ordered set of spans over existing source assets. A span
selects a half-open source frame interval, a gain in decibels, and an optional
crossfade with the preceding span.

Invariants:

- A timeline has at least one span.
- Every referenced source exists and is readable by the timeline's project:
  owned directly or covered by an active grant.
- The timeline persists a `source_authorizations` permission snapshot captured
  at composition time, which rendering re-resolves and re-verifies.
- Source intervals are non-empty and remain inside the source frame count.
- The first span has no crossfade.
- A crossfade cannot exceed the shorter neighboring span.
- Output channel count is explicit and independent from an input source's
  channel count.

### Render job

A render job is the durable record of one timeline execution.

State machine:

`queued -> rendering -> finalizing -> completed`

Any active state may transition to `failed`. `completed` and `failed` are
terminal. A failed job stores a stable error code and message. A completed job
stores the output file, output metadata, and immutable render receipt.

A render carries the consuming `project_id` and may publish only after a
publication gate re-verifies every source authorization. If a grant is revoked
while a render is in progress, the job fails with `authorization_revoked` and
no output or receipt is committed. Artifacts completed before revocation stay
readable, and their receipts still explain the basis that was legal then.

### Render receipt

A receipt records source identifiers, each source's owning project and the
authorization basis (`owned` or `grant`, including the grant identifier), span
placement, gain adjustments, peak and RMS measurements, final normalization
gain, frame count, output digest, and non-fatal warnings. It is written only
after the output bytes have been atomically committed and only once the
publication gate has confirmed every authorization basis is still valid.

## Workflows

### 1. Register audio source

Entry point: `PUT /v1/sources/{source_id}`.

1. The HTTP boundary enforces request size and base64 decoding.
2. The service validates identity fields and decodes the WAV structure.
3. The codec layer verifies PCM format, frame alignment, and signal bounds.
4. Signal measurements are computed from the decoded samples.
5. Storage writes the media bytes and metadata using an atomic commit.

Success returns source metadata. Failure returns a stable validation error and
leaves no visible partial source.

### 2. Compose assembly timeline

Entry point: `PUT /v1/timelines/{timeline_id}`.

1. The HTTP boundary decodes and sizes the span list.
2. Domain validation checks ranges, gain limits, crossfade limits, and temporal
   ordering.
3. The service resolves every source reference from storage.
4. A timeline record is persisted once all spans are valid.

Success returns a ready timeline. Failure reports the first stable validation
issue without persisting a partial timeline.

### 3. Render timeline mixdown

Entry point: `PUT /v1/renders/{render_id}` with a `timeline_id` body field.

1. The service creates a durable `queued` job.
2. The renderer moves to `rendering` and loads all referenced sources.
3. Each source interval is resampled to the timeline sample rate, mapped to the
   requested channel layout, gain-adjusted, faded, and placed on the timeline.
4. Overlapping neighboring spans are mixed with equal-power crossfade weights.
5. The finalizing stage applies peak normalization to the requested target,
   writes a WAV and receipt through temporary files, and renames them into
   place atomically.
6. The job moves to `completed`. Any error records a failed terminal state and
   removes unpublished temporary output.

Success is observable through the job resource, the downloaded WAV, and the
receipt. Failure is observable through the terminal job error code.

### 4. Manage projects, memberships, and grants

1. `PUT /v1/projects/{project_id}` provisions a project with a founding owner.
2. An owner uses `PUT /v1/memberships` to grant actors an owner, editor, or
   viewer role.
3. An owner uses `PUT /v1/grants` to issue a read-only grant over one of the
   project's sources to another project, and `POST /v1/grants/{id}/revoke` to
   withdraw it terminally.
4. All media requests carry `X-Project-Id` and `X-Actor-Id`; the authorization
   guard resolves membership and grant facts for both HTTP entry points and
   internal cross-references, so there is a single permission fact set.

### 5. Adopt pre-isolation data

1. `GET /v1/legacy` enumerates sources, timelines, and jobs that carry no
   project ownership.
2. `POST /v1/legacy/adopt` stamps the authenticated project's ownership onto
   every unowned record in one durable pass, writes an adoption manifest, and
   backfills completed render receipts with an owned-source authorization
   explanation.
3. Already-owned records are never rewritten; unadopted records stay invisible
   to every project.

## HTTP interface

- `GET /healthz`
- `PUT /v1/projects/{project_id}`
- `GET /v1/projects`
- `PUT /v1/memberships`
- `GET /v1/memberships/self`
- `PUT /v1/grants`
- `GET /v1/grants`
- `GET /v1/grants/{id}`
- `POST /v1/grants/{id}/revoke`
- `GET /v1/legacy`
- `POST /v1/legacy/adopt`
- `PUT /v1/sources/{source_id}`
- `GET /v1/sources/{id}`
- `PUT /v1/timelines/{timeline_id}`
- `GET /v1/timelines/{id}`
- `PUT /v1/renders/{render_id}`
- `GET /v1/renders/{id}`
- `GET /v1/renders/{id}/receipt`
- `GET /v1/renders/{id}/output`

Project-scoped routes require `X-Project-Id` and `X-Actor-Id` headers.
Cross-project access denials and missing resources share the same not-found
error codes so object existence is not disclosed.

Errors use a JSON envelope:

```json
{
  "error": {
    "code": "invalid_source_interval",
    "message": "source interval ends beyond the source frame count",
    "field": "spans[0].source_end_frame"
  }
}
```

## Modules

| Module | Responsibility |
|---|---|
| `httpapi` | HTTP routing, identity headers, request limits, JSON encoding, media responses |
| `authz` | Single permission authority: membership, roles, grant resolution, and the render publication gate |
| `service` | Workflow orchestration, permission-aware resource resolution, and job lifecycle |
| `domain` | Entities, project ownership, grants, validation rules, stable errors, state transitions |
| `audiocodec` | WAV parsing and encoding, resampling, channel mapping, signal metrics |
| `render` | Span evaluation, crossfade mixing, normalization, receipt construction |
| `storage` | Atomic project, grant, source, timeline, job, output, receipt persistence and legacy adoption |

## Engineering mechanisms

- State transitions: render jobs persist every active and terminal transition.
- Error propagation: domain errors retain stable codes through HTTP responses.
- Permission enforcement: every resource read and cross-project reference
  resolves through one authorization guard; not-found responses conceal
  cross-project object existence.
- Publication safety: renders re-verify captured authorizations atomically
  before committing artifacts, so a withdrawn grant cannot back a new output.
- Resource lifecycle: temporary files are cleaned after failed publication.
- Concurrency: storage guards indexes, registries, and grant updates; unique
  IDs and atomic renames prevent duplicate or half-visible records.
- Validation boundaries: request size, PCM layout, timeline bounds, and gain
  ranges are checked before allocation-heavy work.
- Data migration: unowned legacy records are enumerated and adopted explicitly
  rather than becoming reachable through a separate outer check.

## Validation plan

The runtime manifest exposes one bounded production smoke path per workflow:

- Register a generated WAV, read it back, and confirm a malformed payload fails.
- Compose a two-span timeline, read it back, and confirm an invalid crossfade
  fails.
- Render a crossfaded timeline, download the output, decode it, and match core
  receipt measurements.

These checks use the real HTTP entry points and local storage. They are not a
replacement for the test suite that the later task stage will add.

Additional production checks cover the authorization boundary:

- `project-isolation`: cross-project reads and references return not-found,
  non-members cannot authenticate, and viewer writes are forbidden.
- `cross-project-grant`: a grantee reads and renders an owned source through a
  grant without any mutation path, while third projects stay isolated.
- `grant-revocation-race`: renders starting after revocation fail without
  artifacts, an in-flight revocation is rejected at the publication gate
  deterministically, and prior completed receipts remain explainable.
- `legacy-adoption`: unowned records are invisible before adoption, adopted
  records become usable by their new project, and owned records are never
  reassigned.
