.PHONY: all build test run lint

all: build

build:
	go build ./...

test:
	go test -v ./...

run:
	go run cmd/api/main.go

lint:
	go vet ./...
