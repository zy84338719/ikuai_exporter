BINARY=bin/ikuai_exporter

.PHONY: clean build run test check

clean:
	@rm -rf bin

build:
	@go mod tidy
	@mkdir -p bin
	@go build -o $(BINARY) .

run: build
	@$(BINARY) -router https://10.10.10.254 -token $(IKUAI_TOKEN) -listen :9100

test:
	@go test ./...

# Same gate as CI: format, vet, then the race-enabled test run.
check:
	@test -z "$$(gofmt -l . )" || { echo "needs gofmt:"; gofmt -l .; exit 1; }
	@go vet ./...
	@go test -race ./...
