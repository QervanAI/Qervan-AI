# agent.Dockerfile - Enterprise Minimal Container Builder
# Build stage 
FROM --platform=$BUILDPLATFORM golang:1.21-alpine3.19 AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-w -s -X main.version=${VERSION}" \
    -o /usr/bin/agent

# Security scan stage
FROM aquasec/trivy:0.45.1 AS scanner 
COPY --from=builder /usr/bin/agent /agent
RUN trivy fs --severity HIGH,CRITICAL --exit-code 1 /

# Final stage  
FROM gcr.io/distroless/static-debian12:latest-nonroot
USER 65534:65534

COPY --from=builder --chown=65534:65534 /usr/bin/agent /app/
COPY --chown=65534:65534 configs/ /app/configs/

EXPOSE 8080 9090
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/app/agent", "healthcheck"]
ENTRYPOINT ["/app/agent"]

# Metadata
LABEL org.opencontainers.image.title="Cirium AI Agent" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="${VCS_URL}" \
      ai.nuzon.telemetry.endpoint="/metrics"
