# A bare Node with ufw installed and, as the package leaves it, inactive.
# Tests turn it on, or make it risky to turn on, before the installer runs.
#
# BASE is this distro's bare image.
ARG BASE
FROM ${BASE}

RUN export DEBIAN_FRONTEND=noninteractive \
 && apt-get update \
 && apt-get install -y --no-install-recommends ufw \
 && rm -rf /var/lib/apt/lists/*
