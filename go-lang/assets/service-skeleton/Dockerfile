# Multi-stage build: compile with the Go toolchain, ship a static binary on a
# distroless, non-root base (no shell, no package manager, tiny attack surface).
#
#   docker build --build-arg VERSION=$(git describe --tags --always) -t orders:dev .

# Build with a supported toolchain at or above the go line in go.mod (the go
# line is a minimum, not the toolchain to use); newer releases carry fixes.
FROM golang:1.27 AS build
WORKDIR /src

# Download modules in their own layer so code edits don't refetch them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080 50051 9090
ENTRYPOINT ["/server"]
