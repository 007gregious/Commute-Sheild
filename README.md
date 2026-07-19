# Commute Shield

Commute Shield is an offline-first, low-data carpooling platform designed for mobile web users in unreliable network conditions. The repository is a monorepo containing a Next.js Progressive Web App, a Go backend that serves native gRPC and gRPC-Web, and a PostgreSQL/PostGIS schema for route matching and telemetry persistence.

## Table of Contents

- [What the Project Does](#what-the-project-does)
- [Architecture](#architecture)
- [Repository Layout](#repository-layout)
- [Tech Stack](#tech-stack)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Backend Service](#backend-service)
- [Web PWA](#web-pwa)
- [Database Schema](#database-schema)
- [API Reference](#api-reference)
- [Development Workflow](#development-workflow)
- [Testing and CI](#testing-and-ci)
- [Operational Notes](#operational-notes)
- [Security and Privacy Notes](#security-and-privacy-notes)

## What the Project Does

Commute Shield helps commuters and drivers coordinate rides while preserving usability during cellular drops and low-bandwidth conditions. The system is built around these product and infrastructure goals:

- **Offline-first ride and booking capture**: the browser stores ride/booking records in IndexedDB before network delivery.
- **Resilient synchronization**: pending records are retried with capped exponential backoff when the device loses connectivity.
- **Low-overhead transport**: browser synchronization is designed around gRPC-Web, while backend RPC handlers can also serve native gRPC on the same HTTP listener.
- **Spatial route matching**: PostGIS indexes and route-matching functions find passenger requests near driver routes within a 500-meter corridor.
- **Telemetry resilience**: vehicle locations are written to Redis geospatial sets for real-time lookup, while high-speed telemetry samples are throttled before PostgreSQL persistence.
- **Data-minimized identity onboarding**: Smile ID verification input is reduced to a SHA-256 identity hash before storage, and erasure requests are protected by HMAC signatures.

## Architecture

```text
Mobile browser / PWA
  ├─ Next.js App Router UI
  ├─ Serwist service worker and runtime caches
  ├─ IndexedDB via Dexie for offline rides/bookings
  └─ gRPC-Web sync client
          │
          ▼
Go backend (:8080)
  ├─ /healthz HTTP health check
  ├─ /identity/onboard JSON endpoint
  ├─ /identity/erase JSON endpoint
  └─ commute.transport.v1.TelemetryService gRPC/gRPC-Web endpoint
          │
          ├─ PostgreSQL/PostGIS through PgBouncer (:6432)
          └─ Redis geospatial cache (:6379)
```

The local Docker Compose environment starts PostGIS, PgBouncer, Redis, and the backend service. The web application is normally run separately with the Next.js development server.

## Repository Layout

```text
.
├── backend/                         # Go backend service
│   ├── cmd/server/                  # HTTP, gRPC-Web, telemetry, identity, and tracing code
│   ├── proto/transport.proto        # TelemetryService protobuf contract
│   ├── .env.example                 # Backend environment template
│   ├── Dockerfile                   # Backend container image
│   ├── go.mod / go.sum              # Go module files
├── supabase/
│   └── migrations/                  # PostgreSQL/PostGIS schema and route matching function
├── web/                             # Next.js PWA
│   ├── src/app/                     # App Router pages, layout, global CSS, and service worker source
│   ├── src/components/              # UI components and service worker registration
│   ├── src/db/                      # Dexie IndexedDB schema and offline queue helpers
│   ├── src/grpc/                    # gRPC-Web sync client adapter
│   ├── src/hooks/                   # Resilient offline synchronization hook
│   ├── src/telemetry/               # OpenTelemetry browser tracer handle
│   ├── public/manifest.json         # PWA manifest
│   └── package.json                 # Web scripts and dependencies
├── docker-compose.yml               # Local PostGIS, PgBouncer, Redis, and backend stack
└── README.md
```

## Tech Stack

### Web

- Next.js 14 App Router
- React 18
- TypeScript
- Dexie / IndexedDB
- Serwist service worker tooling
- OpenTelemetry API for sync tracing hooks

### Backend

- Go 1.24
- `google.golang.org/grpc`
- `github.com/improbable-eng/grpc-web` for in-process gRPC-Web support
- `pgx/v5` and `pgxpool` for PostgreSQL access
- `redis/go-redis/v9` for Redis geospatial writes and throttling
- OpenTelemetry SDK with stdout or OTLP trace export

### Data Infrastructure

- PostgreSQL 16 with PostGIS
- PgBouncer in transaction pooling mode
- Redis 7
- Supabase-compatible SQL migration files

## Prerequisites

Install these tools before local development:

- Docker and Docker Compose
- Go 1.24 or newer
- Node.js 20 or newer
- npm

## Quick Start

### 1. Clone and enter the repository

```bash
git clone <repository-url>
cd Commute-Sheild
```

> Note: the repository directory is currently named `Commute-Sheild` in this workspace.

### 2. Start infrastructure and backend

```bash
docker-compose up -d
```

This starts:

- PostGIS on `localhost:5432`
- PgBouncer on `localhost:6432`
- Redis on `localhost:6379`
- Backend on `localhost:8080`

Verify the backend health endpoint:

```bash
curl http://localhost:8080/healthz
```

Expected response:

```text
ok
```

### 3. Run the web app

```bash
cd web
npm install
npm run dev
```

Open the Next.js app at `http://localhost:3000`.

## Configuration

### Backend Environment Variables

The backend can run with defaults for local Docker infrastructure, but production-like deployments should set the following variables. See `backend/.env.example` for a starting template.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` | Backend HTTP listen address. |
| `DATABASE_URL` | PgBouncer localhost URL | PostgreSQL connection string. Prefer PgBouncer port `6432` with `statement_cache_mode=describe`. |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection URL. |
| `PGPOOL_MIN_CONNS` | `2` | Minimum pgx pool connections. |
| `PGPOOL_MAX_CONNS` | `20` | Maximum pgx pool connections. |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000,http://127.0.0.1:3000` | Comma-separated browser origins allowed to call gRPC-Web endpoints. Set this explicitly in deployed environments. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | OTLP collector endpoint. If empty, spans are written to stdout. |
| `SMILE_ID_BASE_URL` | `https://api.smileidentity.com` | Smile ID API base URL. |
| `SMILE_ID_PARTNER_ID` | empty | Smile ID partner identifier. |
| `SMILE_ID_API_KEY` | empty | Smile ID API key. |
| `SMILE_ID_CALLBACK_URL` | empty | Optional Smile ID callback URL. |
| `SMILE_ID_JOB_TYPE` | `biometric_kyc` | Smile ID job type. |
| `ERASURE_SIGNING_SECRET` | empty | HMAC secret for verified erasure requests. |

### Web Environment Variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `NEXT_PUBLIC_GRPC_WEB_SYNC_URL` | `/api/grpc/sync` | Endpoint used by the browser sync client when delivering offline records. |

## Backend Service

The Go service exposes one HTTP listener, defaulting to `:8080`, and multiplexes regular HTTP routes with gRPC/gRPC-Web traffic.

### Core Behavior

- `GET /healthz` returns `ok` for liveness checks.
- Native gRPC, gRPC-Web, and gRPC-WebSocket requests are served by the wrapped gRPC server.
- State-changing telemetry RPCs require an `x-idempotency-key` metadata header.
- Idempotency records are stored in `commute.api_idempotency_keys` with the RPC method and JSON response payload.
- Redis stores current vehicle locations in a geospatial set named `vehicle_locations`.
- PostgreSQL stores high-speed telemetry samples only when `speed_kmh > 40` and Redis allows the per-vehicle 30-second throttle key.
- OpenTelemetry tracing is initialized during startup. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to export to an OTLP collector, or leave it unset for pretty stdout spans.

### Running the Backend Without Docker Compose

Start PostGIS/PgBouncer/Redis yourself, then run:

```bash
cd backend
cp .env.example .env
# edit .env if needed
set -a && . ./.env && set +a
go run ./cmd/server
```

## Web PWA

The web app is a Next.js PWA shell focused on resilient offline execution.

### Important Web Pieces

- `web/src/db/commuteDb.ts` defines the Dexie database `commute-shield-offline` and the `ridesAndBookings` store.
- `web/src/hooks/useResilientOfflineSync.ts` reads pending records, delivers them through the gRPC-Web client, marks successful records as synced in an IndexedDB transaction, and schedules retry timers for network drops.
- `web/src/components/OfflineSyncStatus.tsx` displays pending count, sync state, errors, and a manual sync button.
- `web/src/app/sw.ts` defines Serwist precaching and runtime caching strategies for documents, assets, fonts, and images.
- `web/src/components/ServiceWorkerRegistrar.tsx` registers `/sw.js` only in production builds.

### Web Commands

```bash
cd web
npm install
npm run dev        # start local development server
npm run typecheck  # run TypeScript type checking
npm run lint       # run Next.js linting
npm run build      # build production app and service worker
npm run start      # serve the production build
```

## Database Schema

The Supabase migration creates and indexes the `commute` schema.

### Main Tables

- `commute.profiles`: user profile records with phone numbers and roles.
- `commute.drivers`: driver vehicle metadata.
- `commute.passengers`: passenger metadata and accessibility notes.
- `commute.driver_routes`: scheduled driver routes represented as PostGIS `LineString` geometries.
- `commute.ride_requests`: passenger pickup/dropoff requests.
- `commute.route_matches`: persisted matches between routes and requests.
- `commute.api_idempotency_keys`: idempotent RPC response cache.
- `commute.vehicle_metric_samples`: throttled telemetry samples with PostGIS point locations.
- `commute.users`: data-minimized identity hashes linked to profiles.

### Route Matching Function

`commute.find_route_passenger_matches` finds open ride requests that:

- fit within available seats,
- satisfy the route departure window,
- have pickup and dropoff points within 500 meters of a scheduled driver route, and
- are ordered by departure time and distance.

## API Reference

### Health Check

```http
GET /healthz
```

Response:

```text
ok
```

### TelemetryService.SubmitTelemetry

Protobuf service:

```proto
service TelemetryService {
  rpc SubmitTelemetry(google.protobuf.Struct) returns (google.protobuf.Struct);
}
```

Required metadata:

```text
x-idempotency-key: <unique-request-key>
```

Request fields:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `vehicle_id` | string | yes | Vehicle identifier. |
| `latitude` | number | yes | Latitude from `-90` to `90`. |
| `longitude` | number | yes | Longitude from `-180` to `180`. |
| `speed_kmh` | number | yes | Non-negative speed in kilometers per hour. |
| `observed_at_unix_ms` | number | no | Observation timestamp in Unix milliseconds. Defaults to server time. |

Response fields:

| Field | Type | Description |
| --- | --- | --- |
| `vehicle_id` | string | Submitted vehicle id. |
| `location_written` | boolean | Whether Redis geospatial write succeeded. |
| `persisted` | boolean | Whether a PostgreSQL sample was stored. |
| `throttle_key` | string | Redis key used for per-vehicle persistence throttling. |

### Identity Onboarding

```http
POST /identity/onboard
Content-Type: application/json
```

Request body:

```json
{
  "user_id": "00000000-0000-0000-0000-000000000000",
  "vnin": "platform-scoped-vnin-token",
  "selfie_base64": "..."
}
```

Behavior:

1. Validates the request shape, vNIN token format, and selfie size.
2. Sends vNIN and selfie data to Smile ID.
3. Extracts verified first name, last name, and date of birth.
4. Validates names locally.
5. Stores only `sha256(first_name + last_name + dob)` in `commute.users.identity_hash`.

Successful response:

```json
{
  "user_id": "00000000-0000-0000-0000-000000000000",
  "identity_hash": "64-character-lowercase-hex-sha256",
  "verified": true
}
```

### Identity Erasure

```http
POST /identity/erase
Content-Type: application/json
x-erasure-signature: <hmac-sha256-user-id>
```

`DELETE /identity/erase` is also accepted.

Request body:

```json
{
  "user_id": "00000000-0000-0000-0000-000000000000"
}
```

The signature is a lowercase hex HMAC-SHA256 of the `user_id` using `ERASURE_SIGNING_SECRET`. A verified user row is locked before the owning profile is deleted transactionally.

Successful response:

```json
{
  "user_id": "00000000-0000-0000-0000-000000000000",
  "erased": true
}
```

## Development Workflow

### Backend

```bash
cd backend
go test ./...
go run ./cmd/server
```

### Web

```bash
cd web
npm install
npm run typecheck
npm run lint
npm run build
```

### Docker Compose

```bash
docker-compose up -d          # start stack
docker-compose logs -f backend
docker-compose down           # stop stack
docker-compose down -v        # stop stack and remove database volume
```

## Testing and CI

The repository contains GitHub Actions workflows for backend and web checks:

- Backend CI runs Go checks for the backend module.
- Web CI runs npm-based checks for the Next.js application.

Recommended local checks before opening a pull request:

```bash
(cd backend && go test ./...)
(cd web && npm install && npm run typecheck && npm run lint && npm run build)
```

## Operational Notes

- Point `DATABASE_URL` at PgBouncer, not directly at PostgreSQL, in environments where transaction pooling is expected.
- Keep `statement_cache_mode=describe` in the PostgreSQL URL for PgBouncer transaction-pool compatibility.
- Service worker registration is intentionally production-only. Use `npm run build && npm run start` to test full PWA behavior locally.
- Redis is part of the telemetry write path. If Redis is unavailable, telemetry submissions will fail before PostgreSQL persistence.
- `OTEL_EXPORTER_OTLP_ENDPOINT` switches tracing from stdout to OTLP export.

## Security and Privacy Notes

- Do not commit `.env`, `.env.local`, Smile ID credentials, erasure signing secrets, or database passwords.
- Identity onboarding should never persist raw vNIN, selfie data, first name, last name, or date of birth. The backend stores only the derived SHA-256 identity hash.
- Erasure requests require `x-erasure-signature`; keep `ERASURE_SIGNING_SECRET` strong and private.
- The current local gRPC-Web origin function permits all origins for development convenience. Restrict allowed origins before production deployment.
- Review PWA caching rules before caching authenticated or user-specific responses.

## Current Limitations and Next Steps

- The web sync client currently posts JSON-shaped gRPC-Web payloads to a configurable endpoint; generated protobuf client bindings can replace this adapter as the API matures.
- The PWA manifest has no icon assets yet.
- Authentication and authorization boundaries are not represented in this repository beyond identity onboarding and erasure primitives.
- The Docker Compose stack runs the backend and data services, while the web app is run separately during local development.
