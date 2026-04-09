.PHONY: build test lint clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/pion-ipc ./cmd/pion-ipc

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ dist/
