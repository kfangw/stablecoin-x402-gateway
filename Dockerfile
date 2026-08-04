# Stage 1: build the CLI binaries as static executables.
FROM golang:1.25-alpine AS build
WORKDIR /src

# Download modules first so this layer is cached across source changes.
COPY go.mod go.sum ./
RUN go mod download

# Build the three real-node binaries into /out. CGO is disabled so the result
# is a fully static binary that runs on a bare alpine image.
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/issuer ./cmd/gateway ./cmd/agent

# Stage 2: minimal runtime image with the binaries and entrypoint scripts.
FROM alpine:3
RUN apk add --no-cache ca-certificates
COPY --from=build /out/issuer /out/gateway /out/agent /usr/local/bin/
COPY docker/entrypoint-init.sh docker/entrypoint-gateway.sh docker/entrypoint-agent.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/entrypoint-init.sh \
             /usr/local/bin/entrypoint-gateway.sh \
             /usr/local/bin/entrypoint-agent.sh
