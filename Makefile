.PHONY: all build run test clean lint

BINARY_NAME=susword-server

all: test build

build:
	go build -o $(BINARY_NAME) ./cmd/server

run:
	go run ./cmd/server

test:
	go test -v -race ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY_NAME)
