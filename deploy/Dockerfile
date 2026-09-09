# Self-hosted Fylane relay. One statically linked binary and one SQLite file:
# the relay stores OAuth and device metadata only, never file content, so the
# whole persistent state is small enough to back up with cp.
#
# Build from the repository root:
#   docker build -f deploy/Dockerfile -t fylane-relay .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-selfhosted
# CGO stays off: the SQLite driver is pure Go, which is what keeps this a
# single binary on any base image.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X fylane/shared/buildinfo.Version=${VERSION}" \
    -o /out/fylane-relay ./relay/cmd/relay

FROM alpine:3.21
RUN adduser -D -H -u 10001 fylane && mkdir -p /var/lib/fylane && chown fylane /var/lib/fylane
COPY --from=build /out/fylane-relay /usr/local/bin/fylane-relay
USER fylane
VOLUME /var/lib/fylane
ENV FYLANE_RELAY_DB=/var/lib/fylane/relay.db
EXPOSE 9000
# -issuer must be the public https URL users reach; it is what the relay
# advertises in its OAuth discovery documents, so a wrong value breaks every
# connector. It is supplied by compose or the run command.
ENTRYPOINT ["/usr/local/bin/fylane-relay", "serve", "-addr", "0.0.0.0:9000"]
