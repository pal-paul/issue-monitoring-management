# Signal Desk

A self-hosted Google Cloud Monitoring inbox. A **Go backend serves the embedded TypeScript frontend**, continuously pulls a consumer-supplied Pub/Sub subscription, and commits incidents to **consumer-managed PostgreSQL** before acknowledging messages.

This public codebase contains no connected GCP project, cloud credentials, default administrator, or Terraform. Consumers own infrastructure and Terraform in their own repositories. **No application environment variables are required:** enter the database, first administrator, and subscription through the frontend. Infrastructure must still supply durable storage, networking, TLS, and runtime identity.

## Local Example: No GCP Required

Requires Docker with Compose. From the repository root:

```sh
docker compose -f example-build/compose.yaml up --build -d
```

Open [http://localhost:8080](http://localhost:8080). In first-run setup, enter this **local-only** database URL and create your own administrator:

```text
postgres://signaldesk:local-development-only@postgres:5432/signaldesk?sslmode=disable
```

The example seeds an empty workspace with synthetic incidents. Pull is paused by default; no GCP credentials or project are needed. In **Connections**, use **Send test incident** to exercise ingestion. It requires an administrator or operator session. Local mode never disables authentication.

The Compose file publishes HTTP and PostgreSQL only on loopback. Its public development database password and HTTP flags are **not production configuration**. The app has no environment section; PostgreSQL's container initialization settings are infrastructure configuration. Remove `-demo` for an empty local inbox. Both the PostgreSQL data and private app configuration use named volumes; rebuilding/replacing containers keeps them.

```sh
docker compose -f example-build/compose.yaml logs -f signaldesk
docker compose -f example-build/compose.yaml restart signaldesk
```

Do not delete either volume. Named volumes are not backups. See [example-build/README.md](example-build/README.md) for image builds and deployment inputs.

## Published Container

[Test and Publish Container](.github/workflows/container.yml) runs PostgreSQL-backed race tests and builds the existing multi-stage Dockerfile. Pull requests validate the build without publishing. Pushes to `main`, `v*` tags, and manual workflow runs publish Linux amd64 images to GitHub Container Registry using the repository's `GITHUB_TOKEN`; no registry password or GCP credentials are required in GitHub secrets.

```text
ghcr.io/pal-paul/issue-monitoring-management:latest
ghcr.io/pal-paul/issue-monitoring-management:sha-FULL_COMMIT_SHA
ghcr.io/pal-paul/issue-monitoring-management:1.2.3
```

Version tags are produced by matching Git tags, for example `v1.2.3`; `latest` follows the default branch. The workflow summary lists the published tags and immutable digest. After the first publish, check the package's GitHub settings and set its visibility to **Public** if it is still private; a public source repository does not guarantee a public package. Prefer an immutable digest for deployments.

For Cloud Run, copy the published image into an existing consumer-owned Artifact Registry Docker repository first. Replace the example region, project, repository, and digest below. Authenticate Docker to GHCR first if the package is private. These commands do not create infrastructure:

```sh
SOURCE='ghcr.io/pal-paul/issue-monitoring-management@sha256:REPLACE_WITH_PUBLISHED_DIGEST'
DESTINATION='europe-west1-docker.pkg.dev/YOUR_PROJECT/YOUR_REPOSITORY/signaldesk:test'
gcloud auth configure-docker europe-west1-docker.pkg.dev
docker pull --platform linux/amd64 "$SOURCE"
docker tag "$SOURCE" "$DESTINATION"
docker push "$DESTINATION"
```

Deploy the resulting Artifact Registry digest with the persistent storage, database connectivity, runtime identity, and always-allocated CPU requirements below. Publishing an image does not deploy GCP resources or supply application credentials. Do not use the local development flags in GCP.

## First Run and Administration

1. Deploy privately with a writable persistent mount at `/data` and network access to PostgreSQL 15+.
2. Open the frontend over HTTPS. Enter the PostgreSQL URL, administrator name, username, and a unique password of 12-72 UTF-8 bytes. The database role must be able to create/alter the app's tables and indexes; migrations run automatically under a database advisory lock.
3. In **Settings**, enter the fully qualified pull subscription: `projects/PROJECT_ID/subscriptions/SUBSCRIPTION_ID`. Enable **Receive messages**, then save. Subscription changes take effect without restarting.
4. In **Users**, create accounts, assign roles, reset passwords, or disable access. The last enabled administrator cannot be disabled or demoted.

**Restrict ingress until the first administrator exists.** Anyone with access to an unclaimed deployment can claim it. If reattaching an already initialized database, setup requires an existing administrator's credentials. Never expose a fresh unclaimed service publicly, even briefly.

| Role          | Read Issues and Timeline | Acknowledge Issues | Add Work Logs | Manage Users and Roles | Manage Settings |
| ------------- | ------------------------ | ------------------ | ------------- | ---------------------- | --------------- |
| Viewer        | Yes                      | No                 | No            | No                     | No              |
| Operator      | Yes                      | Yes                | Yes           | No                     | No              |
| Administrator | Yes                      | Yes                | Yes           | Yes                    | Yes             |

These are three fixed roles with server-enforced permissions, not arbitrary custom permission policies. Editing any account revokes its sessions, including your own if you edit yourself. Passwords are bcrypt-hashed. Opaque 12-hour sessions are stored as hashes in PostgreSQL; production cookies are Secure, HttpOnly, SameSite=Strict. Mutations require the session's CSRF token. Use HTTPS; do not strip the browser's Origin header at a proxy.

## Issue Work Logs and Timeline

Open an issue and enter investigation notes in **Work Log**, then select **Add Work Log**. The formatting toolbar supports headings, bold, italic, strikethrough, links, bullet and numbered lists, quotes, inline code, code blocks, clear formatting, undo, and redo. Administrators and operators can add entries to open, acknowledged, or resolved issues. Entries are append-only, contain 1-10,000 characters of stored Markdown, and retain the signed-in author's username and timestamp. Viewers can read the timeline but cannot add entries. Unsaved drafts survive detail refreshes and reopening within the current page; they are not saved across page reloads or sign-out.

New editor notes persist as Markdown and render with sanitized formatting in the timeline. Raw HTML is displayed literally; images, scripts, and embedded content are not rendered. Links allow only absolute HTTP, HTTPS, and mailto URLs; web URLs containing credentials are rejected. Existing plain-text notes keep their original interpretation, including literal Markdown characters.

Optionally choose **New status**: Open, Acknowledged, or Resolved. The default keeps the current status. This allows resolving or reopening an issue with a required work log. The note, status, timestamps, and attributed status-change timeline entry are saved in one transaction. Failed saves retain the note and selected status. Selecting the current status does not add a redundant transition event.

The **Timeline** shows all persisted work logs, acknowledgements, acknowledgement removals, Monitoring updates, automatic reopenings, resolutions, and stale notifications, newest received first. Monitoring entries include the original occurrence time, message ID, and expandable notification snapshot containing that occurrence's GCP incident ID. Duplicate deliveries do not create duplicate events. Existing history is retained when upgrading; older work logs cannot be reconstructed if they were never recorded.

Work logs and timeline events live in PostgreSQL alongside the issue, not in browser storage or the application container. Replacing the application preserves them when using the same database. Include them in database backups and retention planning.

## Consumer-Owned Cloud Run Deployment

Cloud Run is recommended for the Go service, **not for running its PostgreSQL database**. Consumers provision the following using their own Terraform or deployment tooling:

| Input              | Requirement                                                                                                                                                                       |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Image              | Build [example-build/Dockerfile](example-build/Dockerfile) with the repository root as context; deploy an immutable Linux amd64 image digest.                                     |
| Continuous compute | Minimum **one instance**, preferably maximum one, with **instance-based billing / CPU always allocated**. Request-only CPU allocation can stall background pulling.               |
| Identity           | Attached runtime service account with `roles/pubsub.subscriber` on the selected subscription. No service-account key is uploaded or embedded.                                     |
| Database           | Durable, reachable PostgreSQL 15+ with backups, sufficient storage, and a least-privilege app role. Self-hosting on a persistent-disk VM is supported; Cloud SQL is not required. |
| Bootstrap storage  | Private persistent read/write directory mounted at `/data`, accessible to UID/GID `65532:65532`. It must survive replacement and be shared by service revisions.                  |
| Connectivity       | TLS termination, private/admin-restricted initial ingress, database egress/network routing, and outbound HTTPS to Pub/Sub and runtime credential endpoints.                       |
| Probes             | `/healthz` for process liveness, `/readyz` for database readiness. Readiness intentionally succeeds before setup so onboarding is reachable.                                      |

Set these infrastructure properties explicitly. Minimum instances alone does not allocate CPU between requests, guarantee uptime, or make local files durable. No Terraform, project selection, IAM mutation, or cloud resource creation is performed by this application. Cloud Run supplies `PORT` automatically; otherwise it defaults to 8080. `K_SERVICE` is used only to reject development flags. Neither is an application secret or required manual app configuration.

Cloud Run's writable container filesystem and in-memory volumes disappear during replacement. **Never use them for PostgreSQL or bootstrap settings.** A private Cloud Storage FUSE mount is suitable for the app's small immutable bootstrap JSON generations only; grant narrow object read/write/list access and protect all versions. **Never mount PostgreSQL data files on Cloud Storage/FUSE.** Consumers must provision and test the mount; the application does not create buckets or disks.

Rolling deployments can temporarily overlap revisions even with maximum one instance. All revisions must use the same database and config volume. A PostgreSQL advisory lock coordinates pull batches; transactional deduplication remains necessary because Pub/Sub is at-least-once. A service restart may briefly pause pulling; messages remain in the subscription according to its retention policy.

## Pull Subscription and Monitoring

The consumer provisions a Pub/Sub topic, a **pull subscription** (not a push subscription), and a Monitoring Pub/Sub notification channel. Configure each desired alert policy to use that channel and enable **incident closure notifications**. Grant the Monitoring notification publisher permission to publish to the topic, and the app's runtime identity permission to consume its subscription. The application uses ambient Application Default Credentials only after pulling is enabled.

Use a dedicated subscription, a sensible message retention period, and an acknowledgement deadline of at least **60 seconds**. Configure retry and dead-letter policies, including required Pub/Sub service-agent IAM, in consumer-owned infrastructure. Monitor backlog, oldest unacknowledged message age, dead-letter deliveries, database capacity, and the consumer state shown in **Connections**. Local mode does not need ADC while pull remains paused.

The worker pulls up to ten messages per batch. Valid Monitoring notifications are acknowledged **only after database commit**. Database/acknowledgement failures are safe to redeliver. Malformed messages remain unacknowledged for retry/dead-letter handling; an error appears in Connections. This is at-least-once ingestion with database deduplication, not an exactly-once delivery guarantee.

Message identity is `(subscription, messageId)`; GCP occurrence identity is `(project, incident_id)`. Signal Desk uses a separate **stable issue ID** to group recurring occurrences. A new GCP incident can reopen the same issue without losing work logs or timeline history. The detail view shows both the copyable stable issue ID and the current GCP incident ID; original IDs remain in historical notification snapshots.

New issue IDs are SHA-256 fingerprints of a versioned, canonical JSON identity containing project, policy ID (or policy name), condition name (or display name), metric type (or display name), metric labels, resource type, and resource labels. Map ordering does not affect the fingerprint. Cloud Run's `revision_name` is excluded so a new revision of the same service does not create another issue. If resource labels identify only a location, resource name is included. If project, policy, or resource identity is insufficient, the key falls back to the GCP occurrence ID rather than risking unrelated alerts being merged. Renaming a fallback identity or changing other identity labels can create a different issue; summary, severity, timestamps, and GCP incident ID are not part of a complete stable fingerprint.

Across occurrences, newer `started_at` wins; a different GCP incident with an older or equal start time is retained as stale, even if its closure arrives later or has a later `ended_at`. Within the current occurrence, later event timestamps win, with closure winning ties. A newer open occurrence resets acknowledgement and resolution timestamps, updates the current GCP ID and triggered time, and adds an **Issue reopened** timeline event. Monitoring updates can resolve incidents; administrators and operators can also change status by adding a work log. Manual changes do not advance the Monitoring event timestamp. Severity maps critical/error to Critical, info/informational to Info, and other/missing values to Warning. Historic Monitoring incidents are not imported automatically.

For a real pipeline test, publish the **raw** JSON from [examples/monitoring-alert.json](examples/monitoring-alert.json) to your own topic using your tooling. Its project values are synthetic and its timestamp fixed; use **All time** in the inbox. Pub/Sub supplies the message ID and base64 transport encoding. For closure, reuse the project/incident ID, set `state` to `closed`, and provide `ended_at >= started_at`. There is no public push receiver.

## Persistence, Updates, and Recovery

| Data                                                                                                                                                                                          | Durable Location                                        |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| Incidents, issue fingerprints and GCP occurrence mappings, work logs, timeline/history, delivery deduplication, users, roles, password hashes, sessions, subscription settings, audit entries | Consumer PostgreSQL database                            |
| PostgreSQL URL and workspace identity                                                                                                                                                         | Private `/data/connection-*.json` bootstrap generations |
| Frontend and server executable                                                                                                                                                                | Replaceable image                                       |

Deploy new code against the **same database and persistent config mount**. Schema initialization preserves existing records. Back up both before upgrades and test restoration against a separate deployment. For database backups, use your PostgreSQL backup/PITR tooling; the local example can run `docker compose -f example-build/compose.yaml exec -T postgres pg_dump -U signaldesk signaldesk` with output redirected to a private backup location outside this repository. Do not copy a running PostgreSQL data directory as a backup.

The stable-identity upgrade adds fingerprint and occurrence lookup tables, backfilled from stored notifications. Existing issue IDs, work logs, and timeline events are not rewritten or deleted. If older versions created several issues for the same fingerprint, they remain separate historical records; future new occurrences attach to the issue with the newest stored triggered time (issue ID breaks ties). Notifications for an already-known GCP occurrence stay attached to their original issue. Use the upgraded version consistently across service revisions; older versions do not maintain the new identity mappings.

Bootstrap files contain database credentials: restrict access, encrypt backups/storage, and never commit or serve them. New files are written with mode 0600 in a mode-0700 directory where the filesystem honors POSIX modes; object-storage IAM remains the actual boundary on FUSE. Immutable generations avoid reliance on filesystem rename semantics. Older generations are retained and may contain old credentials: rotate them and apply a consumer-managed version/retention policy that preserves recoverability.

In **Settings > Database connection**, an administrator can save a new URL pointing to the **same workspace**, for example after a database restore or credential rotation. The new connection is validated and persisted; **restart the service to apply it**. A different/empty database is rejected. This action does not copy data. Plan the database copy, cutover, and rollback yourself. Failed startup connections fail closed rather than silently creating a new administrator or losing history.

Restoring an old database backup can lose messages acknowledged after that backup; use PITR and an appropriate recovery objective. Pub/Sub does not automatically repopulate already acknowledged records. Keep backups of both persistence layers and test full replacement recovery. No automatic incident/event/session retention cleanup is implemented; plan retention and capacity for sustained use.

## Source Development

Requires Go 1.24+, Node 20.19+ (or 22.12+), npm, and PostgreSQL 15+ tools on `PATH`:

```sh
make dev
# Or seed a new local workspace:
make demo
```

The helper starts/reuses loopback PostgreSQL in `.local/pgdata` on port 55439, creates `signaldesk_dev`, prints the URL to enter in onboarding, and runs authenticated HTTP on [http://127.0.0.1:8080](http://127.0.0.1:8080). Private bootstrap files live in `.local/config`. PostgreSQL stays running after Go exits; stop it with `pg_ctl -D "$PWD/.local/pgdata" -m fast stop`. `DEV_PG_PORT` is an optional development-helper override, not deployment configuration.

For another local database/config directory, build with `make build` and run `bin/signaldesk -local -data-dir /private/persistent/path`. `-port` overrides the listener. `-demo` seeds only an empty local workspace during first-admin setup. `-local` binds loopback; `-local-container` permits container HTTP and must only be published on loopback. Both preserve login and permissions, enable authenticated `/api/dev/messages`, and are forbidden on Cloud Run. Never use development flags in production.

Use **Connections > Send test incident**, or the helper below (prompts privately for your local account password):

```sh
node scripts/send-sample.mjs http://127.0.0.1:8080 YOUR_USERNAME
```

The helper refuses non-loopback hosts and non-local instances. For frontend hot reload, `npm run dev` proxies `/api` to Go on port 8080; open Vite using the same loopback hostname. `make build` embeds production assets into Go; restart the executable after rebuilding. Node is not required at runtime.

## API and Verification

`GET /api/session` describes setup/session state. `POST /api/setup`, `/api/login`, and `/api/logout` drive access. Authenticated requests use the session cookie; mutations also need `X-CSRF-Token` returned by login/session. Administrator endpoints are `/api/users`, `/api/users/{id}`, `/api/settings`, and `/api/database` (see frontend calls for JSON schemas). No CORS access is enabled. Health probes expose no incident contents.

Issue endpoints: `GET /api/issues`, `GET /api/issues/{id}`, `PATCH /api/issues/{id}` with `{"acknowledged":true|false}`, `GET /api/overview`, and `GET /api/connection`. Resolved incidents cannot be acknowledged. List parameters: `q` (max 200 characters), `status=all|open|acknowledged|resolved`, `severity=all|critical|warning|info`, `project`, inclusive incident-start `from`/`to` RFC3339 timestamps, `page` (1-based), and `limit` (1-100, default 25). Results sort newest incident first. Overview totals are global; Critical counts unacknowledged open critical incidents. Notification documentation is plain text; raw JSONB formatting can differ from incoming JSON.

`POST /api/issues/{id}/worklogs` accepts `{"body":"**Investigation** notes","status":"acknowledged","format":"markdown"}` and requires the `issues.worklog` permission (administrator/operator) plus CSRF. The optional `format` accepts `plain` or `markdown`; omitted or empty defaults to `plain` for existing clients. Leading and trailing whitespace is trimmed before validation and storage. The optional `status` accepts `open`, `acknowledged`, or `resolved`; omitted or empty preserves the current status. The server derives the author from the session; clients cannot override it. The response is the updated issue detail, including its complete `events` timeline. Each event includes `kind`, `actor`, `eventAt`, and `receivedAt`; work logs and `status_changed` events include `body` and `bodyFormat`, while Monitoring events include their historical `payload`. Invalid input returns 400 without persisting the note or status, unauthenticated requests 401, insufficient permission/CSRF 403, and missing issues 404.

```sh
npm ci
npm run build
go vet ./...
go test -race ./...
bash -n scripts/dev.sh
node --check scripts/send-sample.mjs
docker compose -f example-build/compose.yaml config --quiet
```

Real PostgreSQL tests run when the **test-only** `TEST_DATABASE_URL` is set. Use a dedicated local test database with schema-creation permission. Tests create isolated schemas and deliberately leave them intact; never point them at production:

```sh
TEST_DATABASE_URL='postgres://signaldesk@127.0.0.1:55439/signaldesk_test?sslmode=disable' go test -race ./internal/monitor -count=1
```

[scripts/browser-check.mjs](scripts/browser-check.mjs) exports a Playwright-style `(page, ui)` workflow. Run only on a dedicated local `-demo` workspace. For first setup it takes the local database URL from `TEST_DATABASE_URL` and creates a publicly known **test-only** browser administrator. It checks login/setup, user roles, saved paused-pull settings, filtering/pagination, acknowledgement, errors, and desktop/mobile layouts. It leaves test accounts and audit events, writes screenshots to the system temporary directory, and never enables live pulling. Pull commit-before-ack, redelivery, session revocation, last-admin protection, and bootstrap reload are covered by Go integration tests.

## Layout

```text
cmd/signaldesk/       Executable, startup, and shutdown
internal/monitor/     Pull worker, PostgreSQL, accounts, API, and tests
web/src/              TypeScript console and styles
web/dist/             Generated frontend embedded by Go
example-build/        Dockerfile, local Compose, and config examples
scripts/              Local development and browser/test helpers
examples/             Synthetic Monitoring notification fixture
```
