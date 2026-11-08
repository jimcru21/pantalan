BIN := bin/pantalan

.PHONY: build run test vet fmt check

build:
	go build -o $(BIN) ./cmd/pantalan

# Local dev: Dashboard on https://localhost:8443 (self-signed, under
# ./data/tls), proxy on :8080 so it doesn't need root, state under ./data.
run:
	PANTALAN_ADMIN_TOKEN=dev \
	PANTALAN_WEBHOOK_SECRET=dev \
	PANTALAN_DASHBOARD_ADDR=:8443 \
	PANTALAN_PROXY_ADDR=:8080 \
	PANTALAN_DATA_DIR=./data \
	go run ./cmd/pantalan serve

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: vet test
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
