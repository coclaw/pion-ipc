.PHONY: build test test-cover lint check verify dist-npm clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/pion-ipc ./cmd/pion-ipc

test:
	go test -race -count=1 ./...

test-cover:
	go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out
	@echo "--- Coverage threshold check ---"
	@total=$$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $$NF}' | tr -d '%'); \
	threshold=75; \
	if [ $$(echo "$$total < $$threshold" | bc) -eq 1 ]; then \
		echo "FAIL: total coverage $$total% < $$threshold%"; exit 1; \
	else \
		echo "OK: total coverage $$total% >= $$threshold%"; \
	fi

lint:
	golangci-lint run ./...

check: lint

verify: check test-cover

dist-npm:
	bash scripts/build-npm.sh $(VERSION)

clean:
	rm -rf bin/ dist/ coverage.out
