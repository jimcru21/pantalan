# A Node running docker-ce from Docker's apt repo, but older than the 28
# Pantalan needs. The installer must refuse it and never upgrade it. Only
# Debian 12 and Ubuntu 24.04 have a docker-ce < 28 in Docker's repo.
#
# BASE is this distro's bare image.
ARG BASE
FROM ${BASE}

RUN export DEBIAN_FRONTEND=noninteractive \
 && . /etc/os-release \
 && apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && printf 'Types: deb\nURIs: https://download.docker.com/linux/%s\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/docker.asc\n' \
      "$ID" "${UBUNTU_CODENAME:-$VERSION_CODENAME}" "$(dpkg --print-architecture)" \
      > /etc/apt/sources.list.d/docker.sources \
 && apt-get update \
 && old=$(apt-cache madison docker-ce | awk '$3 ~ /^5:27\./ { print $3; exit }') \
 && test -n "$old" \
 && apt-get install -y --no-install-recommends "docker-ce=$old" "docker-ce-cli=$old" containerd.io \
 && systemctl enable docker.service containerd.service \
 && rm -rf /var/lib/apt/lists/*
