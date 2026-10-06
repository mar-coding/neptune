# Builds any NEPTUNE component from source, fully inside Docker.
# Context must be the repository root:
#   docker build -f hack/local/Dockerfile --build-arg COMPONENT=dispatcher -t systemautoscaler/dispatcher:dev .
FROM golang:1.22-bookworm AS builder

ARG COMPONENT
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY pkg ./pkg
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/app ./pkg/${COMPONENT}

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
