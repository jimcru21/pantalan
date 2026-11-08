# The dev image: Go, the docker CLI (with buildx) and git, plus air, which
# rebuilds and restarts `pantalan serve` when a source file changes. The
# source isn't copied in: compose.yaml mounts the checkout at /src. See
# "Develop with Docker" in the README.
FROM docker:28-cli AS docker-cli

FROM golang:1.26-trixie

# The golang image already ships git.
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx

RUN go install github.com/air-verse/air@v1.67.4 \
 && rm -rf /root/.cache/go-build /go/pkg/mod

# Pantalan talks to the Engine API on this fixed path; the dind service
# puts its socket in the shared /var/run/dind volume. The pantalan on PATH
# is the binary air last built, for `docker compose exec pantalan pantalan ...`.
RUN ln -s /var/run/dind/docker.sock /var/run/docker.sock \
 && ln -s /src/tmp/pantalan /usr/local/bin/pantalan

COPY entrypoint.sh /usr/local/bin/pantalan-dev
WORKDIR /src
ENTRYPOINT ["pantalan-dev"]
