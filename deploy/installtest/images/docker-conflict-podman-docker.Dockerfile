# A Node where `docker` is podman-docker, Podman's Docker-compatible CLI.
# The installer must refuse it and never remove it.
#
# BASE is this distro's bare image.
ARG BASE
FROM ${BASE}

RUN export DEBIAN_FRONTEND=noninteractive \
 && apt-get update \
 && apt-get install -y --no-install-recommends podman-docker \
 && rm -rf /var/lib/apt/lists/*
