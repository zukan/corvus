# Corvus

Corvus is a Go service that provides a tenant-scoped subset of the Comment MicroAPI contract, backed by MySQL. It supports organization signup and tokens, application creation and tokens, and comment create/read/list/update/delete. It is inspired by and API-compatible with the public MIT-licensed [comment-microapi](https://github.com/microapidev/comment-microapi) for this supported subset; it does not claim full feature parity.

The HTTP surface has nine `/v1` operations plus `GET /healthz` and `GET /readyz`. Admin management, billing, subscriptions, system-administration endpoints, and reply, vote, or flag mutations are not implemented. Existing reply, flag, up-vote, and down-vote data is preserved during import; comment responses expose the corresponding counts. The service implements the documented subset and does not reproduce legacy abuse bugs.

Deleting a comment is a soft delete and also soft-deletes its retained replies. Updating a comment returns its prior `updatedAt` value in the response, matching the compatible legacy behavior.

## Quick start

Requirements: Docker Compose, or Go 1.26.10 (the module declares Go 1.26.10; Go 1.22+ supports the routing syntax used here). Compose starts a local MySQL 8.4 instance and the API with development-only credentials and secrets:

```sh
docker compose up --build
curl -sS http://localhost:4000/healthz
curl -sS http://localhost:4000/readyz
```

The Compose port is bound to loopback. To run without Compose, start a MySQL database and export `MYSQL_DSN`, `APP_SECRET`, and `ORG_SECRET`, then run `go run ./cmd/corvus`. Both secrets must be standard Base64 values encoding at least 32 bytes. Generate independent random application and organization keys with `openssl rand -base64 32` (run once for each key). Preserve existing keys only if they meet this minimum: already issued tokens remain valid only with the same key material. Weak legacy keys are rejected at startup; rotate them before cutover and arrange token renewal for all clients, since old tokens will stop verifying. There is no weak-key compatibility override. Set `AUTO_MIGRATE=false` when schema changes are managed separately.

The Dockerfile uses BuildKit's target OS and architecture. To build a local multi-platform OCI image archive, use a Buildx builder:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --tag corvus:local --output type=oci,dest=corvus.tar .
```

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `MYSQL_DSN` | required | MySQL connection DSN |
| `APP_SECRET` | required | Standard Base64 application JWT key; at least 32 decoded bytes |
| `ORG_SECRET` | required | Standard Base64 organization JWT key; at least 32 decoded bytes |

All remaining settings and defaults are:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ADDR` | `:4000` | HTTP listen address |
| `TOKEN_TTL` | `720h` | Lifetime of newly issued JWTs |
| `READ_TIMEOUT` | `10s` | Maximum request read duration |
| `READ_HEADER_TIMEOUT` | `5s` | Maximum request-header read duration |
| `WRITE_TIMEOUT` | `15s` | Maximum response write duration |
| `REQUEST_TIMEOUT` | `10s` | Total handler/database context deadline, capped by a shorter `WRITE_TIMEOUT` |
| `IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `SHUTDOWN_TIMEOUT` | `10s` | Startup and graceful-shutdown timeout |
| `MAX_BODY_BYTES` | `1048576` | Maximum request body size; positive integer |
| `RATE_LIMIT` | `120` | Requests per IPv4 address or IPv6 subnet per minute, excluding health checks; positive integer |
| `IPV6_RATE_LIMIT_PREFIX` | `64` | IPv6 subnet prefix length used for throttling; integer from 1 through 128 |
| `CORS_ORIGINS` | empty | Comma-separated allowed origins; empty disables cross-origin access |
| `DISABLE_AUTH` | `true` | Legacy inverted name: `true` requires bearer credentials in middleware. API handlers still require an Authorization header and valid tenant claims, so setting `false` does not make the API operations anonymous. |
| `AUTO_MIGRATE` | `true` | Apply the service's MySQL schema migrations at startup |

The API accepts JSON request bodies. `page` defaults to 1, `limit` defaults to 20 and must be from 1 through 50. Identifiers are 24-character hexadecimal strings. Comment updates and deletes also require the matching `ownerId`; application and organization claims scope all comment access to their tenant. Rate limits use the remote socket IP and ignore forwarded-IP headers. CORS is allowed only for origins listed in `CORS_ORIGINS`; compressed request bodies receive HTTP 415.

Request deadlines propagate through authentication lookups, repository queries, and transactions, and preserve earlier client deadlines. MySQL connections default to a 5-second dial timeout and 10-second read/write timeouts; positive `timeout`, `readTimeout`, and `writeTimeout` DSN values override these defaults. Socket write timeouts alone do not cancel database work.

The in-process rate limiter retains at most 10,000 address/subnet buckets. It removes expired buckets and then evicts the least recently used bucket to admit a new client, so a full table does not reject every unseen client. A single IPv6 subnet shares one bucket. Sustained churn across many distinct subnets can evict active buckets and reset their windows; distributed abuse control belongs at the ingress as well. Forwarded-address headers are not trusted.

## MongoDB migration

The migration command reads `organizations`, `admins`, `applications`, `comments`, and `replies` from MongoDB. First run a dry run to validate documents and count records without connecting to MySQL or writing data:

```sh
MONGO_URI='mongodb://localhost:27017' MONGO_DATABASE='legacy' \
  go run ./cmd/corvus-migrate-mongo --dry-run
```

For an actual import, stop writes to the source service and take a restorable backup before switching. Then provide the target DSN:

```sh
MONGO_URI='mongodb://localhost:27017' MONGO_DATABASE='legacy' \
MYSQL_DSN='user:password@tcp(localhost:3306)/corvus?parseTime=true' \
  go run ./cmd/corvus-migrate-mongo
```

The importer preserves record IDs, created/updated/deletion timestamps, soft-delete state, and the stored reply, flag, up-vote, and down-vote arrays. Imports are insert-only: existing IDs with identical parent bindings are skipped, even if ordinary content differs, and never overwritten; conflicting unique keys with a different ID fail the import. An existing ID with a different organization or parent is rejected: admins must match `organization_id`, applications must match both `organization_id` and `created_by`, comments must match `application_id`, and replies must match `comment_id`. This target check runs in `PutImported` at the first conflicting insert, after source preflight and schema migration; it stops before importing that record's children. Existing rows are never overwritten or moved. Schema creation and inserts affect MySQL only. The dry run reads and validates source records but does not contact or write to MySQL, so it cannot detect target binding conflicts. Review the printed read/inserted/skipped counts before routing clients to the new service. On failure, the command prints accumulated counts on stdout and a sanitized collection/record location on stderr; earlier successful inserts remain committed and can be skipped on a restart.

The orphan policy is to reject the entire source if an admin or application has no organization, an application creator is missing or belongs to another organization, a comment has no application, or a reply has no comment. Soft-deleted parents still count as existing relationships. These relationships are checked across all five collections before dry-run success or any MySQL schema/insertion work. Repair or deliberately remove orphaned source records before retrying; they are never silently dropped. The real import stages source BSON in a mode-0600 temporary file, reads back the validated snapshot for insertion, and removes that file on exit. Allow enough temporary disk space for the source data; relationship indexes remain in memory. Dry-run does not stage document payloads to disk.

Imported bcrypt credentials retain their original first-72-byte verification semantics, including legacy passwords longer than 72 bytes. Newly created credentials still reject passwords longer than 72 bytes. A legacy password reset is not required solely because of its length; rotate credentials separately if their security is in doubt.

## Checks

```sh
go build ./...
go vet ./...
go test ./...
go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
staticcheck ./...
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
govulncheck ./...
```

These match CI. CI selects Go 1.26.10 from `go.mod`, staticcheck v0.8.2, and govulncheck v1.1.4. The MySQL integration test has the `integration` build tag and is excluded from `go test ./...`. Run it against a disposable local database with Compose:

```sh
docker compose up -d mysql
MYSQL_TEST_DSN='corvus_demo:local_demo_password@tcp(127.0.0.1:3306)/corvus?parseTime=true' \
  go test -tags=integration ./internal/repository -run TestMySQLIntegrationRepositoryFlows -count=1
```

The integration test creates records in the selected database; do not point it at production data. The MongoDB-to-MySQL integration test additionally requires `MONGO_TEST_URI` pointing to a disposable MongoDB 8 instance. With both test variables configured, run `go test -count=1 -tags=integration ./...`; it validates dry-run behavior, all five collections, preserved timestamps and arrays, and repeat imports. Set `MYSQL_READY_TEST_DSN` to a disposable MySQL server account allowed to create/drop databases to additionally exercise readiness with missing schema versions, tables, and columns. The tagged suite also checks stalled-query cancellation and orphan rejection before target writes. Without the required test variables, the integration tests skip cleanly. OpenAPI is described in [`api/openapi.yaml`](api/openapi.yaml). Upstream license attribution is in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

The compatibility suite compares 13 request/response pairs derived from the upstream contract fixture, plus caller-shaped `refId` pagination for ascending and descending pages, multiple posts, and an empty post. Each case checks the complete records and pagination metadata.

## HTTP API

The full request and response contract is in [`api/openapi.yaml`](api/openapi.yaml). Success responses use `{ "data": ..., "status": "success", "message": "..." }`; errors use `{ "status": "error", "error": "...", "data": ... }`. Health endpoints return `{ "status": "ok" }`; readiness returns HTTP 503 when MySQL cannot be reached, the latest embedded schema migration is absent, or required tables/columns are inaccessible. Readiness checks are read-only. With `AUTO_MIGRATE=false`, apply schema migrations before routing traffic.
