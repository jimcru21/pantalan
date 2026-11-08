# A Node running the distro's own Docker package, docker.io, which Pantalan
# refuses: it wants Docker CE from Docker's apt repo and never removes a
# Docker for you.
#
# BASE is this distro's bare image.
ARG BASE
FROM ${BASE}

RUN export DEBIAN_FRONTEND=noninteractive \
 && apt-get update \
 && apt-get install -y --no-install-recommends docker.io \
 && rm -rf /var/lib/apt/lists/*
