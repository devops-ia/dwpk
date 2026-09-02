# Build the gateway binary
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
# Stamped into the binary so a running pod reports its own version,
# rather than leaving it to be inferred from an image tag.
ARG VERSION=dev

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -ldflags "-s -w -X main.version=${VERSION}" -o gateway cmd/gateway/main.go

# Use distroless as minimal base image to package the gateway binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/gateway .
USER 65532:65532

ENTRYPOINT ["/gateway"]
