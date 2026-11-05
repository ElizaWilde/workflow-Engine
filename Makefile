.PHONY: setup-wsl run build test race integration integration-local check generate

setup-wsl:
	./scripts/setup-wsl.sh

run:
	./scripts/with-env.sh go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./...

race:
	CGO_ENABLED=1 go test -race ./...

integration:
	./scripts/with-env.sh go test -tags integration -count=1 ./...

integration-local:
	./scripts/with-env.sh ./scripts/integration-local.sh

check:
	go vet ./...
	go test ./...

generate:
	go run github.com/99designs/gqlgen generate
