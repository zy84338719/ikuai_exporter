FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
# go.mod has a local replace (../ikuai-api) for development. In the image
# build the sibling repo isn't present, so drop the replace and resolve
# ikuai-api from the module proxy at the published version, then tidy.
RUN go mod edit -dropreplace=github.com/zy84338719/ikuai-api && \
    go get github.com/zy84338719/ikuai-api@v1.1.1 && \
    go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ikuai_exporter .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/ikuai_exporter /ikuai_exporter
EXPOSE 9100
ENTRYPOINT ["/ikuai_exporter"]
