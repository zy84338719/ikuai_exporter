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

# Same gate as CI: format, vet, the race-enabled test run, then a build
# against the published dependencies only.
#
# The GOWORK=off build is the gate that catches "go.mod's direct/indirect
# markers disagree with the source imports" — the class of breakage that only
# shows up in CI and in the Docker image build, never in a workspace build.
check:
	@test -z "$$(gofmt -l . )" || { echo "needs gofmt:"; gofmt -l .; exit 1; }
	@go vet ./...
	@go test -race ./...
	@GOWORK=off go build ./...
