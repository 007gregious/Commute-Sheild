package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	integrationDriverProfileID    = "11111111-1111-1111-1111-111111111111"
	integrationDriverID           = "22222222-2222-2222-2222-222222222222"
	integrationPassengerProfileID = "33333333-3333-3333-3333-333333333333"
	integrationPassengerID        = "44444444-4444-4444-4444-444444444444"
	integrationRouteID            = "55555555-5555-5555-5555-555555555555"
)

type integrationHarness struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	redis    *redis.Client
	tracer   trace.Tracer
	recorder *tracetest.SpanRecorder
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(oldProvider)
	})

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL/PostGIS integration database is unavailable: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &integrationHarness{ctx: ctx, pool: pool, tracer: otel.Tracer("commuteshield.integration_tests"), recorder: recorder}
	h.applyMigration(t)
	h.resetRows(t)
	return h
}

func (h *integrationHarness) applyMigration(t *testing.T) {
	t.Helper()
	_, span := h.tracer.Start(h.ctx, "test.migration.apply", trace.WithAttributes(attribute.String("db.system", "postgresql")))
	defer span.End()
	migrationPath := filepath.Clean("../../../supabase/migrations/20260607000000_init_postgis_schema.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration %s: %v", migrationPath, err)
	}
	if _, err := h.pool.Exec(h.ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
}

func (h *integrationHarness) resetRows(t *testing.T) {
	t.Helper()
	_, span := h.tracer.Start(h.ctx, "test.fixtures.reset")
	defer span.End()
	_, err := h.pool.Exec(h.ctx, `
		delete from commute.route_matches;
		delete from commute.ride_requests where passenger_id = $1;
		delete from commute.driver_routes where id = $2;
		delete from commute.passengers where id = $1;
		delete from commute.drivers where id = $3;
		delete from commute.profiles where id in ($4, $5);
		delete from commute.api_idempotency_keys where key like 'integration-%';
		delete from commute.vehicle_metric_samples where vehicle_id like 'integration-%';
	`, integrationPassengerID, integrationRouteID, integrationDriverID, integrationDriverProfileID, integrationPassengerProfileID)
	if err != nil {
		t.Fatalf("reset fixture rows: %v", err)
	}
}

func (h *integrationHarness) seedRoute(t *testing.T, seats int) {
	t.Helper()
	_, span := h.tracer.Start(h.ctx, "test.fixtures.seed_route", trace.WithAttributes(attribute.Int("ride.available_seats", seats)))
	defer span.End()
	_, err := h.pool.Exec(h.ctx, `
		insert into commute.profiles (id, full_name, phone_number, role)
		values ($1, 'Integration Driver', '+2348000000001', 'driver') on conflict (id) do nothing;
		insert into commute.drivers (id, profile_id, vehicle_make, vehicle_model, license_plate, seat_capacity)
		values ($2, $1, 'Toyota', 'Sienna', 'INT-001', 4) on conflict (id) do nothing;
		insert into commute.driver_routes (id, driver_id, origin_label, destination_label, departure_time, route_path, available_seats, fare_amount, status)
		values ($3, $2, 'Ikeja City Mall', 'Victoria Island', now() + interval '30 minutes',
			st_setsrid(st_makeline(array[
				st_makepoint(3.3446, 6.6118),
				st_makepoint(3.3514, 6.4281)
			]), 4326), $4, 2500, 'scheduled')
		on conflict (id) do update set available_seats = excluded.available_seats, route_path = excluded.route_path, status = 'scheduled';
	`, integrationDriverProfileID, integrationDriverID, integrationRouteID, seats)
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}
}

func TestPostGISRouteLookupRadiusCasting(t *testing.T) {
	h := newIntegrationHarness(t)
	h.seedRoute(t, 3)
	ctx, span := h.tracer.Start(h.ctx, "test.spatial.radius_casting", trace.WithAttributes(attribute.Float64("lookup.radius_km", 5)))
	defer span.End()

	var within5km int
	if err := h.pool.QueryRow(ctx, `
		select count(*) from commute.driver_routes
		where id = $1 and st_dwithin(route_path::geography, st_setsrid(st_makepoint(3.3213, 6.5774), 4326)::geography, $2::double precision)
	`, integrationRouteID, 5000).Scan(&within5km); err != nil {
		t.Fatalf("query 5km radius: %v", err)
	}
	if within5km != 1 {
		t.Fatalf("expected airport point lookup within 5km to return route, got %d", within5km)
	}

	var within200m int
	if err := h.pool.QueryRow(ctx, `
		select count(*) from commute.driver_routes
		where id = $1 and st_dwithin(route_path::geography, st_setsrid(st_makepoint(3.3213, 6.5774), 4326)::geography, $2::double precision)
	`, integrationRouteID, 200).Scan(&within200m); err != nil {
		t.Fatalf("query 200m radius: %v", err)
	}
	if within200m != 0 {
		t.Fatalf("expected tightened 200m lookup to filter out route, got %d", within200m)
	}
}

func TestIdempotencyKeyReplaysIdenticalResponseWithoutDuplicateInsert(t *testing.T) {
	h := newIntegrationHarness(t)
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}
	redisClient, err := connectRedis(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	if err := redisClient.Ping(h.ctx).Err(); err != nil {
		t.Skipf("Redis integration dependency is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	svc := &server{db: &tracedDB{pool: h.pool, tracer: h.tracer}, redis: redisClient, tracer: h.tracer}
	interceptor := idempotencyUnaryInterceptor(svc.db)
	key := "integration-aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	req, _ := structpb.NewStruct(map[string]any{"vehicle_id": "integration-ride-create", "latitude": 6.6118, "longitude": 3.3446, "speed_kmh": 55, "observed_at_unix_ms": float64(time.Now().UnixMilli())})
	call := func() *structpb.Struct {
		ctx := metadata.NewIncomingContext(h.ctx, metadata.Pairs(idempotencyHeader, key))
		ctx, span := h.tracer.Start(ctx, "test.idempotency.call")
		defer span.End()
		resp, err := interceptor(ctx, req, &grpc.UnaryServerInfo{FullMethod: fullSubmitMethod}, func(ctx context.Context, req any) (any, error) {
			return svc.SubmitTelemetry(ctx, req.(*structpb.Struct))
		})
		if err != nil {
			t.Fatalf("idempotent call: %v", err)
		}
		return resp.(*structpb.Struct)
	}
	first, second := call(), call()
	firstJSON, _ := protojson.Marshal(first)
	secondJSON, _ := protojson.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("responses differ: %s != %s", firstJSON, secondJSON)
	}
	var samples, keys int
	_ = h.pool.QueryRow(h.ctx, `select count(*) from commute.vehicle_metric_samples where vehicle_id = 'integration-ride-create'`).Scan(&samples)
	_ = h.pool.QueryRow(h.ctx, `select count(*) from commute.api_idempotency_keys where key = $1`, key).Scan(&keys)
	if samples != 1 || keys != 1 {
		t.Fatalf("expected one sample and one idempotency key, got samples=%d keys=%d", samples, keys)
	}
}

func TestConcurrentBookingsOnlyOneConsumesLastSeat(t *testing.T) {
	h := newIntegrationHarness(t)
	h.seedRoute(t, 1)
	const attempts = 8
	var successes, outOfSeats atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, span := h.tracer.Start(h.ctx, "test.booking.concurrent_attempt", trace.WithAttributes(attribute.Int("attempt", i)))
			defer span.End()
			err := bookSeat(ctx, h.pool, integrationRouteID)
			if err == nil {
				successes.Add(1)
				return
			}
			if err.Error() == "out of seats" {
				outOfSeats.Add(1)
				return
			}
			t.Errorf("unexpected booking error: %v", err)
		}(i)
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 || outOfSeats.Load() != attempts-1 {
		t.Fatalf("expected 1 success and %d out-of-seats responses, got %d and %d", attempts-1, successes.Load(), outOfSeats.Load())
	}
}

func bookSeat(ctx context.Context, pool *pgxpool.Pool, routeID string) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var seats int
	if err := tx.QueryRow(ctx, `select available_seats from commute.driver_routes where id = $1 for update`, routeID).Scan(&seats); err != nil {
		return err
	}
	if seats < 1 {
		return fmt.Errorf("out of seats")
	}
	if _, err := tx.Exec(ctx, `update commute.driver_routes set available_seats = available_seats - 1 where id = $1`, routeID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
