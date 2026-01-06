FROM golang:1.25-alpine AS base

FROM base AS builder
ENV SOURCE_DATE_EPOCH=0
WORKDIR /build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=bind,source=.,target=. \
    CGO_ENABLED=0 GOOS=linux go build -v -ldflags="-s -w" -trimpath -o /podrefresh .

# Final image
FROM scratch
COPY --link --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --link --from=builder /podrefresh /podrefresh
ENTRYPOINT ["/podrefresh"]
