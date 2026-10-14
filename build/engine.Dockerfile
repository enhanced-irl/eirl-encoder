# Build image for eirl-engine (cgo, linux/arm64): Ubuntu 18.04 like L4T R32.7.6, so the binary links
# against the same glibc 2.27, GLib 2.56 and GStreamer 1.14.5 as the board. The base plugins are there
# for the tests of internal/gst (videotestsrc, appsrc/appsink); the NVIDIA elements only exist on the board.
#   docker build --platform linux/arm64 -f build/engine.Dockerfile -t eirl-engine-build build
FROM ubuntu:18.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential pkg-config ca-certificates curl \
      libglib2.0-dev libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
      gstreamer1.0-plugins-base gstreamer1.0-alsa \
    && rm -rf /var/lib/apt/lists/*
ARG GO_VERSION=1.26.4
ARG GO_SHA256=ef758ae7c6cf9267c9c0ef080b8965f453d89ab2d25d9eb22de4405925238768
RUN curl -fsSL -o /tmp/go.tgz https://go.dev/dl/go${GO_VERSION}.linux-arm64.tar.gz \
    && echo "${GO_SHA256}  /tmp/go.tgz" | sha256sum -c - \
    && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
ENV PATH=/usr/local/go/bin:$PATH GOPATH=/go GOCACHE=/go/cache GOFLAGS=-buildvcs=false CGO_ENABLED=1
WORKDIR /src
