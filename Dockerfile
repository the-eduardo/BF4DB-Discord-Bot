# syntax=docker/dockerfile:1

# Build stage. TARGETARCH comes from buildx, so the same Dockerfile produces
# amd64 and arm64 images — the old build hardcoded amd64 and would not run on
# an ARM host.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/bf4db-bot .

# Final stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 bot

COPY --from=builder /out/bf4db-bot /usr/local/bin/bf4db-bot

USER bot
ENTRYPOINT ["/usr/local/bin/bf4db-bot"]
