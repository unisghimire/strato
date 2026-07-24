# Multi-stage build: buf codegen → compile → distroless runtime.
# One image serves all three binaries; the entrypoint selects at run time.

# --- Stage 1: protobuf codegen ---
FROM bufbuild/buf:1.47.2 AS proto
WORKDIR /src
COPY buf.yaml buf.gen.yaml buf.lock ./
COPY proto/ proto/
RUN buf generate

# --- Stage 2: build ---
FROM golang:1.23-alpine AS build
WORKDIR /src

# Dependency layer cache: go.mod/go.sum change far less than source.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
COPY --from=proto /src/proto/gen proto/gen
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/migrate ./cmd/migrate

# --- Stage 3: runtime ---
# Distroless: no shell, no package manager, tiny attack surface. Runs as
# nonroot (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /out/worker /out/migrate /app/
COPY configs/ /app/configs/
COPY migrations/ /app/migrations/

EXPOSE 8080 9090 9100
USER nonroot
ENTRYPOINT ["/app/server"]
CMD ["--config", "/app/configs/config.yaml"]
