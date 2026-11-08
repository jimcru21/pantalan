# A Node that already runs Docker the way Pantalan accepts it: docker-ce 28
# or newer from Docker's apt repo, enabled, with no daemon.json. The
# installer must use it untouched.
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
 && apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io \
 && systemctl enable docker.service containerd.service \
 && rm -rf /var/lib/apt/lists/*
