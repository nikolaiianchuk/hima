.PHONY: generate build test run lint

generate:
	go tool templ generate

build: generate
	go build -o bin/hima ./cmd/hima

test: generate
	go test ./...

run: generate
	go run ./cmd/hima -config hima.yaml

lint:
	go vet ./...
	staticcheck ./...
