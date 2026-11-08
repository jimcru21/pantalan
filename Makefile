BIN := bin/pantalan

.PHONY: build run test vet fmt check shellcheck installtest installtest-vm

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
