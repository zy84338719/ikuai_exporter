BINARY=bin/ikuai_exporter

.PHONY: clean build run

clean:
	@rm -rf bin

build:
	@go mod tidy
	@mkdir -p bin
	@go build -o $(BINARY) .

run: build
	@$(BINARY) -router http://10.10.10.254 -username admin -password admin -listen :9100
