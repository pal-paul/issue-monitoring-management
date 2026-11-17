# Consumer Build Examples

Build from the **repository root**, not this directory:

```sh
docker build -f example-build/Dockerfile -t signaldesk:local .
```

For Cloud Run, build a Linux amd64 image, push it to your own registry with your own tooling, and deploy its immutable digest. The image contains only the Go executable, embedded frontend, certificates, and a writable `/data` directory owned by UID/GID 65532. PostgreSQL is external. No project, credentials, subscription, or admin account is baked into it.

The consumer supplies persistent `/data`, durable PostgreSQL, TLS/private initial ingress, runtime IAM, and Cloud Run minimum-one/always-allocated CPU settings. The default executable needs no application environment variables or command arguments. Cloud Run supplies its standard `PORT`. See the [deployment and persistence requirements](../README.md).

## Offline Local Example

```sh
docker compose -f example-build/compose.yaml up --build -d
```

Open [http://localhost:8080](http://localhost:8080) and enter this development-only URL in the first-run form:

```text
postgres://signaldesk:local-development-only@postgres:5432/signaldesk?sslmode=disable
```

Choose your own administrator credentials. The app seeds synthetic incidents; pull stays paused. **Connections > Send test incident** works without a GCP project. The Docker PostgreSQL port is loopback-only 55440, avoiding the native helper's 55439. Change the loopback host port in Compose if 8080 is already occupied.

`compose.yaml` is **local-only**: it uses a public example PostgreSQL password, HTTP development flags, and sample data. Do not deploy it to Cloud Run or expose its ports publicly. It mounts separate named volumes for PostgreSQL and config; keep both across rebuilds. Do not use `down -v` unless you intend to destroy all local workspace data.

## Configuration Examples

[onboarding.example.json](onboarding.example.json) illustrates the first-run API body, and [pull-settings.example.json](pull-settings.example.json) illustrates a paused pull setting. Replace placeholders with consumer values when using the API. These files are **not loaded automatically**; normal configuration is entered in the UI. Do not put real passwords, bootstrap files, private configs, or infrastructure state in this repository.

Image tags in the Dockerfile float to current patch releases; pin reviewed image digests in your consumer build process and scan/rebuild regularly. No Terraform or cloud resource creation is included.
