# syntax=docker/dockerfile:1

# Build on the host's architecture and cross-compile, so a multi-platform
# build doesn't run the Go compiler under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
# -trimpath keeps the build machine's paths out of the binary.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o /out/evalwright ./cmd/evalwright

# A static binary needs no OS: distroless has CA certificates and a
# non-root user, and no shell or package manager to attack.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/evalwright /usr/local/bin/evalwright
COPY examples/*.yaml /work/examples/
WORKDIR /work
ENTRYPOINT ["evalwright"]
CMD ["--help"]
