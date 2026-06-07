package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	grpcweb "github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	idempotencyHeader = "x-idempotency-key"
	fullSubmitMethod  = "/commute.transport.v1.TelemetryService/SubmitTelemetry"
)

type appConfig struct {
	Addr         string
	DatabaseURL  string
	RedisURL     string
	OtelEndpoint string
}

type txKey struct{}

type tracedDB struct {
	pool   *pgxpool.Pool
	tracer trace.Tracer
}

type server struct {
	db       *tracedDB
	redis    *redis.Client
	tracer   trace.Tracer
	identity *identityRouter
}

type telemetryInput struct {
	VehicleID string
	Latitude  float64
	Longitude float64
	SpeedKMH  float64
	Observed  time.Time
}

func main() {
	ctx := context.Background()
	cfg := loadConfig()

	shutdownTracer, err := initTracing(ctx, cfg)
	if err != nil {
		log.Fatalf("initialize tracing: %v", err)
	}
	defer shutdownTracer(context.Background())

	pool, err := connectPgBouncerPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect postgres pool: %v", err)
	}
	defer pool.Close()

	redisClient, err := connectRedis(cfg.RedisURL)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer redisClient.Close()

	db := &tracedDB{pool: pool, tracer: otel.Tracer("commuteshield.db")}
	identityCfg := loadIdentityConfig()
	identityRouter := &identityRouter{
		db:     db,
		smile:  newSmileIDRESTClient(identityCfg),
		cfg:    identityCfg,
		tracer: otel.Tracer("commuteshield.identity"),
	}
	svc := &server{db: db, redis: redisClient, tracer: otel.Tracer("commuteshield.telemetry"), identity: identityRouter}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(idempotencyUnaryInterceptor(db)))
	registerTelemetryService(grpcServer, svc)

	wrapped := grpcweb.WrapServer(
		grpcServer,
		grpcweb.WithOriginFunc(func(origin string) bool { return true }),
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wrapped.IsGrpcWebRequest(r) || wrapped.IsGrpcWebSocketRequest(r) || r.ProtoMajor == 2 {
			wrapped.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		case "/identity/onboard":
			svc.identity.ServeOnboard(w, r)
			return
		case "/identity/erase":
			svc.identity.ServeErase(w, r)
			return
		default:
			http.NotFound(w, r)
		}
	})

	log.Printf("CommuteShield backend listening on %s with native gRPC and gRPC-Web", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, handler); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func loadConfig() appConfig {
	return appConfig{
		Addr:         env("ADDR", ":8080"),
		DatabaseURL:  env("DATABASE_URL", "postgres://postgres:postgres@localhost:6432/postgres?sslmode=disable&pool_max_conns=20&statement_cache_mode=describe"),
		RedisURL:     env("REDIS_URL", "redis://localhost:6379/0"),
		OtelEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func connectPgBouncerPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "commuteshield-backend"
	cfg.MinConns = int32(parseIntEnv("PGPOOL_MIN_CONNS", 2))
	cfg.MaxConns = int32(parseIntEnv("PGPOOL_MAX_CONNS", 20))
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pool, pool.Ping(ctx)
}

func parseIntEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func connectRedis(redisURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
}

func initTracing(ctx context.Context, cfg appConfig) (func(context.Context) error, error) {
	var exporter sdktrace.SpanExporter
	var err error
	if cfg.OtelEndpoint != "" {
		exporter, err = otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(cfg.OtelEndpoint), otlptracegrpc.WithInsecure())
	} else {
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	}
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.Default()),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown, nil
}

func idempotencyUnaryInterceptor(db *tracedDB) grpc.UnaryServerInterceptor {
	stateChangingMethods := map[string]func() proto.Message{
		fullSubmitMethod: func() proto.Message { return &structpb.Struct{} },
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		responseFactory, stateChanging := stateChangingMethods[info.FullMethod]
		if !stateChanging {
			return handler(ctx, req)
		}

		key, err := idempotencyKeyFromMetadata(ctx)
		if err != nil {
			return nil, err
		}

		tx, err := db.begin(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "begin idempotency transaction: %v", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()

		stored, found, err := db.lookupIdempotencyKey(ctx, tx, key, info.FullMethod)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "lookup idempotency key: %v", err)
		}
		if found {
			message := responseFactory()
			if err := protojson.Unmarshal(stored, message); err != nil {
				return nil, status.Errorf(codes.Internal, "decode idempotent response: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, status.Errorf(codes.Internal, "commit idempotency replay: %v", err)
			}
			committed = true
			return message, nil
		}

		resp, err := handler(context.WithValue(ctx, txKey{}, tx), req)
		if err != nil {
			return nil, err
		}

		message, ok := resp.(proto.Message)
		if !ok {
			return nil, status.Error(codes.Internal, "idempotent response is not a protobuf message")
		}
		payload, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode idempotent response: %v", err)
		}
		if err := db.insertIdempotencyKey(ctx, tx, key, info.FullMethod, payload); err != nil {
			return nil, status.Errorf(codes.Internal, "store idempotency key: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, status.Errorf(codes.Internal, "commit idempotency transaction: %v", err)
		}
		committed = true
		return resp, nil
	}
}

func idempotencyKeyFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Errorf(codes.InvalidArgument, "metadata header %q is required", idempotencyHeader)
	}
	values := md.Get(idempotencyHeader)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", status.Errorf(codes.InvalidArgument, "metadata header %q is required", idempotencyHeader)
	}
	return strings.TrimSpace(values[0]), nil
}

func (db *tracedDB) begin(ctx context.Context) (pgx.Tx, error) {
	ctx, span := db.tracer.Start(ctx, "db.begin", trace.WithAttributes(attribute.String("db.system", "postgresql")))
	defer span.End()
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	recordSpanError(span, err)
	return tx, err
}

func (db *tracedDB) lookupIdempotencyKey(ctx context.Context, tx pgx.Tx, key, method string) ([]byte, bool, error) {
	ctx, span := db.tracer.Start(ctx, "db.api_idempotency_keys.select", trace.WithAttributes(attribute.String("rpc.method", method)))
	defer span.End()
	var payload []byte
	err := tx.QueryRow(ctx, `
		select response_payload
		from commute.api_idempotency_keys
		where key = $1 and rpc_method = $2
		for update
	`, key, method).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	recordSpanError(span, err)
	return payload, err == nil, err
}

func (db *tracedDB) insertIdempotencyKey(ctx context.Context, tx pgx.Tx, key, method string, payload []byte) error {
	ctx, span := db.tracer.Start(ctx, "db.api_idempotency_keys.insert", trace.WithAttributes(attribute.String("rpc.method", method)))
	defer span.End()
	_, err := tx.Exec(ctx, `
		insert into commute.api_idempotency_keys (key, rpc_method, response_payload)
		values ($1, $2, $3)
	`, key, method, payload)
	recordSpanError(span, err)
	return err
}

func (db *tracedDB) insertTelemetrySample(ctx context.Context, tx pgx.Tx, input telemetryInput) error {
	ctx, span := db.tracer.Start(ctx, "db.vehicle_metric_samples.insert", trace.WithAttributes(attribute.String("vehicle.id", input.VehicleID)))
	defer span.End()
	_, err := tx.Exec(ctx, `
		insert into commute.vehicle_metric_samples (vehicle_id, speed_kmh, observed_at, location)
		values ($1, $2, $3, st_setsrid(st_makepoint($4, $5), 4326))
	`, input.VehicleID, input.SpeedKMH, input.Observed, input.Longitude, input.Latitude)
	recordSpanError(span, err)
	return err
}

func recordSpanError(span trace.Span, err error) {
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		return
	}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

type telemetryServiceServer interface {
	SubmitTelemetry(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func registerTelemetryService(grpcServer *grpc.Server, svc *server) {
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "commute.transport.v1.TelemetryService",
		HandlerType: (*telemetryServiceServer)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "SubmitTelemetry",
			Handler:    submitTelemetryHandler(svc),
		}},
		Streams:  []grpc.StreamDesc{},
		Metadata: "proto/transport.proto",
	}, svc)
}

func submitTelemetryHandler(svc *server) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		req := &structpb.Struct{}
		if err := dec(req); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return svc.SubmitTelemetry(ctx, req)
		}
		info := &grpc.UnaryServerInfo{Server: svc, FullMethod: fullSubmitMethod}
		handler := func(ctx context.Context, req any) (any, error) {
			return svc.SubmitTelemetry(ctx, req.(*structpb.Struct))
		}
		return interceptor(ctx, req, info, handler)
	}
}

func (s *server) SubmitTelemetry(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	ctx, span := s.tracer.Start(ctx, "telemetry.submit")
	defer span.End()

	input, err := parseTelemetry(req)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.String("vehicle.id", input.VehicleID), attribute.Float64("vehicle.speed_kmh", input.SpeedKMH))

	if err := s.writeLocationVector(ctx, input); err != nil {
		return nil, status.Errorf(codes.Internal, "write location vector: %v", err)
	}

	persisted := false
	throttleKey := fmt.Sprintf("telemetry:persist:%s", input.VehicleID)
	if input.SpeedKMH > 40 {
		allowed, err := s.redis.SetNX(ctx, throttleKey, input.Observed.UnixMilli(), 30*time.Second).Result()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "evaluate telemetry persistence throttle: %v", err)
		}
		if allowed {
			tx, ok := ctx.Value(txKey{}).(pgx.Tx)
			if !ok {
				return nil, status.Error(codes.Internal, "missing transactional context")
			}
			if err := s.db.insertTelemetrySample(ctx, tx, input); err != nil {
				return nil, status.Errorf(codes.Internal, "persist telemetry sample: %v", err)
			}
			persisted = true
		}
	}

	resp, err := structpb.NewStruct(map[string]any{
		"vehicle_id":       input.VehicleID,
		"location_written": true,
		"persisted":        persisted,
		"throttle_key":     throttleKey,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build response: %v", err)
	}
	return resp, nil
}

func parseTelemetry(req *structpb.Struct) (telemetryInput, error) {
	values := req.AsMap()
	input := telemetryInput{
		VehicleID: strings.TrimSpace(stringField(values, "vehicle_id")),
		Latitude:  numberField(values, "latitude"),
		Longitude: numberField(values, "longitude"),
		SpeedKMH:  numberField(values, "speed_kmh"),
		Observed:  time.Now().UTC(),
	}
	if raw := numberField(values, "observed_at_unix_ms"); raw > 0 {
		input.Observed = time.UnixMilli(int64(raw)).UTC()
	}
	if input.VehicleID == "" {
		return input, status.Error(codes.InvalidArgument, "vehicle_id is required")
	}
	if input.Latitude < -90 || input.Latitude > 90 {
		return input, status.Error(codes.InvalidArgument, "latitude must be between -90 and 90")
	}
	if input.Longitude < -180 || input.Longitude > 180 {
		return input, status.Error(codes.InvalidArgument, "longitude must be between -180 and 180")
	}
	if input.SpeedKMH < 0 {
		return input, status.Error(codes.InvalidArgument, "speed_kmh must be non-negative")
	}
	return input, nil
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func numberField(values map[string]any, key string) float64 {
	switch value := values[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func (s *server) writeLocationVector(ctx context.Context, input telemetryInput) error {
	_, span := s.tracer.Start(ctx, "redis.geoadd", trace.WithAttributes(attribute.String("vehicle.id", input.VehicleID)))
	defer span.End()
	_, err := s.redis.GeoAdd(ctx, "vehicle_locations", &redis.GeoLocation{
		Name:      input.VehicleID,
		Longitude: input.Longitude,
		Latitude:  input.Latitude,
	}).Result()
	recordSpanError(span, err)
	return err
}
