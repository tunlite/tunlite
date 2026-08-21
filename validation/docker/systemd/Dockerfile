FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV container=docker

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        dbus-user-session \
        jq \
        libpam-systemd \
        openssh-client \
        openssh-server \
        procps \
        python3 \
        systemd \
        systemd-sysv \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 2000 --shell /bin/bash tunlite

COPY validation/docker/systemd/setup.sh /usr/local/sbin/tunlite-e2e-setup
RUN chmod 0755 /usr/local/sbin/tunlite-e2e-setup

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
