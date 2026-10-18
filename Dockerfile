FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY . .
# go.mod pins ikuai-api to its published version and carries no local replace,
# so the build context is self-contained. GOWORK=off is belt-and-braces: a stray
# go.work pointing at ../ikuai-api would fail here, where that path cannot exist.
ENV GOWORK=off
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ikuai_exporter .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/ikuai_exporter /ikuai_exporter
EXPOSE 9100
ENTRYPOINT ["/ikuai_exporter"]
