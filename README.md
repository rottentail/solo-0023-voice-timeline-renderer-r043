# Voice Timeline Renderer

Voice Timeline Renderer is a self-hosted Go service for turning validated PCM
WAV recordings into a crossfaded, peak-normalized mixdown. The service exposes
an idempotent HTTP surface, keeps media and metadata under one data directory,
and records every render transition.

The implementation covers:

- 16-bit integer PCM WAV validation and signal measurement.
- Ordered timeline spans with source-frame selection, gain, and crossfade.
- Linear sample-rate conversion and mono/stereo channel mapping.
- Equal-power neighboring-span crossfades.
- Peak normalization into a requested dBFS target.
- Durable render states, receipts, and atomically committed output folders.
- Project ownership and role-based membership with default cross-project
  isolation.
- Read-only cross-project source grants, terminal revocation, and a
  publication gate that never releases an artifact whose authorization was
  withdrawn.
- A legacy adoption path that gives pre-isolation data explicit ownership and
  backfills historical render receipts.

## Project isolation and authorization model

Every source, timeline, and render belongs to exactly one project. Requests
carry the authenticated identity the deployment's fronting proxy establishes:

- `X-Project-Id`: the project in whose scope the request runs.
- `X-Actor-Id`: the acting member inside that project.

Membership roles are `owner`, `editor`, and `viewer`. Editors and above create
sources, timelines, and renders; owners additionally manage memberships and
issue or revoke grants. A viewer may read project resources but not write.

### Cross-project source grants

A project that owns a source may grant another project read-only use of it.
The grantee can read the source and place it inside its own timelines and
renders, but can never modify the source or its measured facts; the owning
project remains the sole authority over them.

Each timeline stores a permission snapshot (`source_authorizations`) captured
at composition time. Rendering re-resolves every source through the same
authorization guard and re-verifies the snapshots immediately before
publishing artifacts:

- If a grant is revoked before the render starts, the source is no longer
  readable and the render fails without creating artifacts.
- If revocation meets an in-progress render, the publication gate rejects the
  job (`authorization_revoked`) and no output or receipt is released.
- A render completed while the grant was active stays readable, and its
  receipt continues to explain the basis that was legal at that time.

### No existence disclosure

Cross-project reads use the same `*_not_found` responses as genuinely missing
objects. An unauthorized project cannot distinguish "the object exists in
another project" from "the object does not exist". Output and receipt
downloads are project-scoped through the owning render job.

### Legacy data adoption

Records written before multi-project isolation carry no project ownership and
are invisible to every project until explicitly adopted. An owner can list the
unowned records and adopt them into their project in one durable pass. Adoption
stamps ownership on sources, timelines, and jobs, writes an adoption manifest,
and backfills completed render receipts so historical legal use remains
explainable. Already-owned objects are never reassigned.

Automated tests are intentionally deferred. A later task stage will add unit,
integration, and end-to-end verification around the production interfaces.

## Requirements

- Go 1.23 or newer.
- A writable local directory for source media, timeline metadata, render jobs,
  output WAV files, and receipts.

No network service, database server, or third-party Go module is required.

## Build

```bash
go build ./...
```

## Run

```bash
go run ./cmd/voiceservice \
  -addr :8093 \
  -data ./var/voice-timeline
```

The health endpoint is `GET http://127.0.0.1:8093/healthz`.

Runtime settings are supplied through arguments:

- `-addr`: HTTP listen address, default `:8093`.
- `-data`: persistent data root, default `./var/voice-timeline`.

No environment variable is required. Do not put credentials in the repository;
the service does not consume any remote credential.

## HTTP interface

All project-scoped requests require the `X-Project-Id` and `X-Actor-Id`
identity headers. Project and membership provisioning, grants, and legacy
adoption are available alongside the media workflows:

- `PUT /v1/projects/{project_id}` — provision a project with a founding owner.
- `GET /v1/projects` — list projects in which the actor is a member.
- `PUT /v1/memberships` — owner-only add or update a member role.
- `GET /v1/memberships/self` — read the caller's own role.
- `PUT /v1/grants` — owner-only issue a read-only source grant.
- `GET /v1/grants` and `GET /v1/grants/{grant_id}` — list or read visible grants.
- `POST /v1/grants/{grant_id}/revoke` — owner-only terminal revocation.
- `GET /v1/legacy` — owner-only list of unowned pre-isolation records.
- `POST /v1/legacy/adopt` — owner-only adoption into the caller's project.

### Register a source

`PUT /v1/sources/{source_id}`

```json
{
  "name": "opening take",
  "wav_base64": "UklGR..."
}
```

The source ID is chosen by the caller and must be 3-64 characters using
letters, digits, underscore, or hyphen. Repeating the same ID with exactly the
same name and bytes is idempotent. Reusing it with different content returns
the `source_conflict` error.

Response:

```json
{
  "id": "opening_take_01",
  "name": "opening take",
  "digest": "b7c0...",
  "byte_count": 96044,
  "sample_rate": 48000,
  "channels": 1,
  "bits_per_sample": 16,
  "frame_count": 48000,
  "duration_ms": 1000,
  "peak_dbfs": -8.0,
  "rms_dbfs": -11.0,
  "created_at": "2026-11-09T12:00:00Z"
}
```

### Compose a timeline

`PUT /v1/timelines/{timeline_id}`

```json
{
  "name": "two part narration",
  "sample_rate": 48000,
  "output_channels": 2,
  "target_peak_dbfs": -1,
  "spans": [
    {
      "source_id": "opening_take_01",
      "source_start_frame": 0,
      "source_end_frame": 48000,
      "gain_db": -2,
      "crossfade_frames": 0
    },
    {
      "source_id": "closing_take_01",
      "source_start_frame": 0,
      "source_end_frame": 44100,
      "gain_db": 0,
      "crossfade_frames": 4800
    }
  ]
}
```

Spans are sequential. A later span overlaps its predecessor by
`crossfade_frames`; the overlap is mixed with equal-power weights. A crossfade
must fit within both neighboring spans.

### Start a render

`PUT /v1/renders/{render_id}`

```json
{
  "timeline_id": "two_part_narration_01"
}
```

The render runs through the durable states
`queued -> rendering -> finalizing -> completed`. A failed render records a
terminal `failed` state with a stable error code and does not expose a partial
output folder.

### Read resources

- `GET /v1/sources/{source_id}`
- `GET /v1/timelines/{timeline_id}`
- `GET /v1/renders/{render_id}`
- `GET /v1/renders/{render_id}/receipt`
- `GET /v1/renders/{render_id}/output`

The output route returns `audio/wav`. The receipt records source digests, span
placement, channel mapping, measured peak and RMS amplitude, normalization
gain, output digest, and non-fatal warnings.

## Data directory

```text
var/voice-timeline/
  projects/<project_id>.json     # project record with its member list
  grants/registry.json           # all cross-project grants and revocation state
  sources/<source_id>/
    source.wav
    metadata.json                # includes project_id ownership
  timelines/<timeline_id>.json   # includes project_id and authorization snapshot
  jobs/<render_id>.json          # includes project_id
  renders/<render_id>/
    output.wav
    receipt.json                 # explains each source's authorization basis
  legacy/adoption-*.json         # durable legacy ownership adoption manifests
  tmp/
```

Source and render folders are assembled in `tmp/` and moved into place with one
directory rename. Metadata updates use same-directory temporary files followed
by rename.

## Source layout

```text
cmd/voiceservice/       service entry point
cmd/timelinecheck/      workflow execution entry point
internal/authz/         project membership, role, and grant permission guard
internal/httpapi/       routing, identity headers, request limits, JSON errors
internal/service/       workflow orchestration and idempotency
internal/domain/        entities, ownership, validation, errors, transitions
internal/audiocodec/    WAV handling, transforms, and signal measurement
internal/render/        timeline preparation, mixing, normalization, receipt
internal/storage/       atomic local repository and legacy adoption
internal/checkflow/     bounded production workflow checks
```

## Workflow checks

Run each declared production check from the repository root:

```bash
go run ./cmd/timelinecheck -workflow ingest-audio-source
go run ./cmd/timelinecheck -workflow compose-timeline
go run ./cmd/timelinecheck -workflow render-mixdown
go run ./cmd/timelinecheck -workflow project-isolation
go run ./cmd/timelinecheck -workflow cross-project-grant
go run ./cmd/timelinecheck -workflow grant-revocation-race
go run ./cmd/timelinecheck -workflow legacy-adoption
```

Each check creates an isolated temporary data directory, starts the real HTTP
handler, sends generated WAV inputs, and verifies read-back or rendered
output. The security checks additionally exercise role enforcement,
not-found-equivalent cross-project access, grant-based rendering, a
deterministic revocation-vs-publication interleaving, and legacy adoption. The
checks are bounded and do not require network access.

## Deferred test boundary

This baseline contains no unit tests, test fixtures, browser checks, or
`test_command`. The downstream task stage owns test creation and red/green
verification. It should extend coverage around WAV chunk edge cases, timeline
crossfade bounds, failed job cleanup, concurrent idempotency, and HTTP error
mapping.
