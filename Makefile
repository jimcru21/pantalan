BIN := bin/pantalan

.PHONY: build run test vet fmt check shellcheck installtest

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
	go vet -tags installtest ./deploy/installtest/

fmt:
	gofmt -w .

check: vet test
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

shellcheck:
	shellcheck deploy/install.sh

# The installer harness: boots privileged systemd containers (needs Docker)
# and runs deploy/install.sh in them. INSTALLTEST_DISTROS=debian-12 picks
# one distro; see deploy/installtest/doc.go.
installtest:
	go test -tags installtest -count=1 -v -timeout 30m ./deploy/installtest/
