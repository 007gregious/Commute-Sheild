module github.com/commuteshield/backend

go 1.24

require (
	github.com/improbable-eng/grpc-web v0.15.0
	github.com/jackc/pgx/v5 v5.7.2
	github.com/redis/go-redis/v9 v9.7.1
	go.opentelemetry.io/otel v1.34.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.34.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.34.0
	go.opentelemetry.io/otel/sdk v1.34.0
	google.golang.org/grpc v1.70.0
	google.golang.org/protobuf v1.36.5
)
