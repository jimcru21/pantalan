# A bare supported Node: what a minimal Debian or Ubuntu cloud server ships
# with (systemd as PID 1, sshd, curl, iproute2, sudo) and nothing Pantalan
# needs installed for it: no Docker, no git.
#
# BASE is the distro image, e.g. debian:12 or ubuntu:26.04.
ARG BASE
FROM ${BASE}

ENV container=docker
RUN export DEBIAN_FRONTEND=noninteractive \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus curl ca-certificates openssh-server iproute2 sudo \
 && rm -rf /var/lib/apt/lists/*

# Units that make no sense in a container or slow down its boot. The
# image's policy-rc.d (which stops packages starting services) goes too:
# servers don't have one.
RUN systemctl mask \
      getty.target console-getty.service systemd-firstboot.service \
      systemd-udevd.service systemd-udev-trigger.service \
      systemd-networkd-wait-online.service \
 && rm -f /etc/machine-id /var/lib/dbus/machine-id /usr/sbin/policy-rc.d \
 && install -d -m 0700 /root/.ssh

# Nested dockerd needs real filesystems under it, not overlayfs.
VOLUME ["/var/lib/docker", "/var/lib/containerd"]

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
