BIN := bin/pantalan

.PHONY: build run test vet fmt check gofmt-check types types-check web-deps web-check web web-dev smoke smoke-docker shellcheck installtest installtest-vm

# The binary with the SPA embedded: the frontend first, then Go. A plain
# `go build` without Node also works; that binary serves a "frontend not
# built" page instead of the Dashboard.
build: web
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

# Every Go file outside web/app's node_modules, which go.mod's ignore
# keeps out of ./... but gofmt doesn't know about.
GOFILES = find . -name '*.go' -not -path './web/app/node_modules/*'

fmt:
	$(GOFILES) -exec gofmt -w {} +

# The one gate: the Go checks, then the frontend's type check, linters and
# tests. The Go-only targets (build, test, vet) never need Node.
check: vet test gofmt-check web-check

gofmt-check:
	@out=$$($(GOFILES) -exec gofmt -l {} +); test -z "$$out" || (echo "gofmt needed:"; echo "$$out"; exit 1)

# The API's TypeScript types, generated from the Go structs (tygo.yaml).
types:
	go tool tygo generate

# For CI: fails when the committed generated types aren't what the Go
# structs generate now.
types-check: types
	@test -z "$$(git status --porcelain -- web/app/src/api/generated)" || \
		(echo "stale API types: run make types and commit the result"; git status --porcelain -- web/app/src/api/generated; git diff -- web/app/src/api/generated; exit 1)

# The frontend's dependencies, from the lockfile, with install scripts off
# so no package runs code at install. Reinstalls when the lockfile changes.
WEB_DEPS = web/app/node_modules/.package-lock.json
$(WEB_DEPS): web/app/package-lock.json
	cd web/app && npm ci --ignore-scripts
	@touch $@
web-deps: $(WEB_DEPS)

web-check: $(WEB_DEPS)
	cd web/app && npm run typecheck && npm run lint && npm test

# The SPA, built into web/dist, which the binary embeds (web/embed.go).
web: $(WEB_DEPS)
	cd web/app && npm run build

# The Playwright smoke tests (web/app/e2e) against the binary with its
# embedded frontend: they start bin/pantalan on free loopback ports with a
# fresh data dir. They need Chromium and its system libraries, installed
# once with `cd web/app && npx playwright install --with-deps chromium`;
# smoke-docker needs neither.
smoke: build
	cd web/app && npm run smoke

# The smoke tests in Playwright's own image, which has Chromium and its
# libraries, at the version in package.json. The binary is built static so
# it runs in the image.
PLAYWRIGHT_VERSION = $(shell node -p "require('./web/app/package.json').devDependencies['@playwright/test']")
smoke-docker:
	CGO_ENABLED=0 $(MAKE) build
	docker run --rm --init --ipc=host --user "$$(id -u):$$(id -g)" -e HOME=/tmp -e CI \
		-v "$(CURDIR):/repo" -w /repo/web/app \
		mcr.microsoft.com/playwright:v$(PLAYWRIGHT_VERSION)-noble npm run smoke

# The SPA's dev server with hot reload, beside `make run`, on
# https://localhost:5173 (with make run's certificate once it has made
# one), proxying /api to it.
web-dev: $(WEB_DEPS)
	cd web/app && npm run dev

shellcheck:
	shellcheck deploy/install.sh deploy/release.sh dev/entrypoint.sh dev/setup-vps.sh

# The installer harness: boots privileged systemd containers (needs Docker)
# and runs deploy/install.sh in them. INSTALLTEST_DISTROS=debian-12 picks
# one distro; see deploy/installtest/doc.go.
installtest:
	go test -tags installtest -count=1 -v -timeout 30m ./deploy/installtest/

# The VM tier (nightly and the release gate): the VM-only paths, plus the
# ssh.socket path, on KVM VMs from the distros' cloud images. Needs x64,
# /dev/kvm, libvirt's default network, virt-install, qemu-img and
# cloud-localds (see .github/workflows/installer-vm.yml); without them every
# path skips, unless INSTALLTEST_VM_REQUIRED is set.
INSTALLTEST_VM_PATHS = ^(TestVM.*|TestSSHSocketOnAnotherPortIsAllowed)$$
installtest-vm:
	INSTALLTEST_SUBSTRATE=vm go test -tags installtest -count=1 -v -timeout 38m \
		-run '$(INSTALLTEST_VM_PATHS)' ./deploy/installtest/
