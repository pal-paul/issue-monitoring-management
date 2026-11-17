.PHONY: build test dev demo check docker fmt lint

build:
	npm ci
	npm run build
	go build -trimpath -o bin/signaldesk ./cmd/signaldesk

test:
	go test -race ./...

check:
	npm run build
	go vet ./...
	go test -race ./...

dev: build
	bash scripts/dev.sh

demo: build
	bash scripts/dev.sh -demo

docker:
	docker build -f example-build/Dockerfile -t signaldesk:local .

fmt:    ## run the configured Go formatters
	golangci-lint fmt

lint:   ## run golangci-lint on the source files
	golangci-lint run
