# Project CommuteShield Monorepo (Web-First PWA)

## Architecture Rulebook for AI Coding Agents

This project is an Offline-First, Low-Data Carpooling web application built for mobile browsers in the Nigerian infrastructure landscape.

### System Constraints

1. **Offline-First via IndexedDB**: The user interface interacts directly with the browser's IndexedDB (via Dexie.js).
2. **Background Sync**: Network operations must run asynchronously inside Service Workers to handle volatile internet drops.
3. **Data Serialization**: Real-time communication must prioritize binary wire layouts over heavy text JSON streams.

### Structure Mapping

- `/backend` -> Go microservice tracking spatial queries.
- `/web` -> Next.js PWA client utilizing TypeScript and Tailwind CSS.
- `/supabase` -> Raw PostgreSQL + PostGIS structural schema files.

### To Spin Up Local Dev Testing Environment

```bash
docker-compose up -d
```

---

## Backend gRPC / gRPC-Web Microservice

The Go backend exposes `commute.transport.v1.TelemetryService` on a single primary
HTTP listener. Native gRPC and browser gRPC-Web requests are handled in-process
by `github.com/improbable-eng/grpc-web/go/grpcweb`; no standalone Envoy or
middleware proxy process is required for local development.

State-changing RPCs must send an `x-idempotency-key` metadata header. The unary
interceptor replays previously stored JSON responses from
`commute.api_idempotency_keys`; new calls execute inside a PostgreSQL
transaction, persist the response payload, and commit atomically.

Telemetry submissions are accepted as a `google.protobuf.Struct` with these
fields:

- `vehicle_id` (string)
- `latitude` (number)
- `longitude` (number)
- `speed_kmh` (number)
- `observed_at_unix_ms` (number, optional)

Each submission updates Redis real-time vehicle position data with `GEOADD` on
`vehicle_locations`. PostgreSQL telemetry sample storage is throttled to at most
once every 30 seconds per vehicle when `speed_kmh` is above 40 km/h.

`DATABASE_URL` should target PgBouncer, typically port `6432`, with
`statement_cache_mode=describe` for transaction-pool compatibility. OpenTelemetry
tracing is initialized at startup; set `OTEL_EXPORTER_OTLP_ENDPOINT` to export to
an OTLP collector, or leave it unset for stdout spans.

## Identity Onboarding and NDPA Erasure

The backend also exposes two HTTP JSON endpoints next to the gRPC-Web handler:

- `POST /identity/onboard` accepts `user_id`, a platform-scoped `vnin`, and a `selfie_base64` payload. The service forwards the vNIN and selfie to the Smile ID verification boundary, extracts only verified first name, last name, and date of birth, validates the names, computes `sha256(first_name + last_name + dob)`, and stores only the resulting 64-character lowercase hex digest in `commute.users.identity_hash`.
- `POST` or `DELETE /identity/erase` accepts `user_id` and requires `x-erasure-signature`, a lowercase HMAC-SHA256 signature of the user id using `ERASURE_SIGNING_SECRET`. A verified `commute.users` row is locked before the owning profile is deleted transactionally to support an explicit NDPA-style data-subject erasure request.

Configure Smile ID and erasure signing with `SMILE_ID_BASE_URL`, `SMILE_ID_PARTNER_ID`, `SMILE_ID_API_KEY`, `SMILE_ID_CALLBACK_URL`, `SMILE_ID_JOB_TYPE`, and `ERASURE_SIGNING_SECRET`.
