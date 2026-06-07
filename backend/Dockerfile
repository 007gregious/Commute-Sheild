FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/commuteshield ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/commuteshield /commuteshield
EXPOSE 8080
ENTRYPOINT ["/commuteshield"]
