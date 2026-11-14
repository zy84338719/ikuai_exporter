FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY . .
# go.mod pins ikuai-api to its published version and carries no local replace,
# so the build context is self-contained. GOWORK=off is belt-and-braces: a stray
# go.work pointing at ../ikuai-api would fail here, where that path cannot exist.
ENV GOWORK=off

# Version metadata for app_build_info.
#
# oekit/core/version ships "unknown" defaults, so an image built without these
# reports branch=unknown, build_time=unknown, commit=unknown in its own
# metrics — a collector that cannot say which build it is running. CI passes
# real values in via --build-arg; a plain local docker build keeps the
# defaults, which is what a developer's own machine should show.
ARG APP_VERSION=dev
ARG GIT_COMMIT=unknown
ARG GIT_BRANCH=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X github.com/oekit/core/version.Version=${APP_VERSION} -X github.com/oekit/core/version.BuildCommit=${GIT_COMMIT} -X github.com/oekit/core/version.BuildBranch=${GIT_BRANCH} -X github.com/oekit/core/version.BuildTime=${BUILD_TIME}" \
      -o ikuai_exporter .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/ikuai_exporter /ikuai_exporter
EXPOSE 9100
ENTRYPOINT ["/ikuai_exporter"]
