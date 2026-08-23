# Deployment Readiness Assessment

This repository is close to deployable for a prototype/PWA demo, but it is not yet fully production-ready. The checks below capture the current status and the highest-priority remediation work.

## Fixed in this pass

- Docker Compose now starts the backend in addition to PostGIS and Redis, applies the initial PostGIS schema on first database initialization, waits for healthy infrastructure before starting the backend, and wires service-to-service `DATABASE_URL`/`REDIS_URL` values.
- Generated web build artifacts are ignored so local `next build` output does not pollute source control.
- README deployment instructions now match the actual Compose topology instead of referencing a PgBouncer service that was not present.

## Current readiness status

| Area | Status | Notes |
| --- | --- | --- |
| Web static build | Ready with caveats | `npm ci`, `npm run typecheck`, `npm run lint`, and `npm run build` pass. Build output is generated in `web/out`. |
| Backend tests/build | Blocked by dependency checksum state | `backend/go.sum` is missing required checksum entries. Network policy returned 403 while attempting to repair with `go mod tidy`, so the Go test/build cannot be completed in this environment. |
| Local Docker Compose | Improved, not fully verified | Compose now includes backend and health checks, but backend image build still depends on a complete `go.sum`. |
| Database schema | Prototype-ready | The migration defines the application schema and PostGIS functions, but production migration execution should be handled by a release pipeline rather than Docker init scripts alone. |
| Security/secrets | Needs production hardening | Defaults are development-oriented. Production must set Smile ID credentials, erasure signing secret, database credentials, Redis credentials/TLS if applicable, and explicit CORS origins. |
| Web dependency freshness | Needs follow-up | The installed Next.js version emits a security warning during install. Package registry access returned 403 when checking the latest allowed patch, so a dependency upgrade could not be completed here. |
| gRPC-Web client contract | Needs integration verification | The frontend currently posts JSON to a configurable endpoint while the backend exposes a generic gRPC-Web-wrapped protobuf service. End-to-end browser sync should be tested against the deployed backend before production launch. |

## Required before production launch

1. Regenerate and commit a complete `backend/go.sum` in an environment with access to the approved Go module proxy.
2. Run `go test ./...` and build the backend container successfully.
3. Upgrade Next.js to a patched version allowed by your package policy and rerun the web checks.
4. Validate the browser offline-sync flow against the actual backend gRPC-Web endpoint, including idempotency metadata.
5. Move development credentials out of Compose for shared environments; use deployment secrets instead.
6. Add CI that runs backend tests, web typecheck/lint/build, and container builds on every PR.
7. Add production observability configuration for OTLP traces, database/Redis health, and service-level alerting.

## Environment-specific limitations encountered

- Go module and npm registry requests returned HTTP 403 for several dependencies. That prevented fully repairing `go.sum` and prevented checking/upgrading to the latest Next.js patch in this environment.
