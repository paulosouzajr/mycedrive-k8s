# Dockerfile to build the DMTCP container image.
# Provides the dmtcp_coordinator binary and exposes the full DMTCP
# installation under /share so application sidecars can mount it
# without embedding the ~200 MB binaries in their own images.
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update -q && apt-get -qy install --no-install-recommends \
      build-essential  \
      git              \
      make             \
      python3          \
      python3-pip      \
      ca-certificates  \
  && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /dmtcp /tmp

WORKDIR /dmtcp
RUN git clone --depth 1 https://github.com/dmtcp/dmtcp.git /dmtcp

RUN ./configure --prefix=/usr && make -j"$(nproc)" && make install

# Copy all installed DMTCP files into /share so an initContainer can
# populate an emptyDir volume mounted by the application containers.
RUN mkdir -p /share/bin /share/lib
RUN cp -r /usr/bin/dmtcp_* /share/bin/ && \
    cp -r /usr/lib/dmtcp   /share/lib/  2>/dev/null || true

# Checkpoint output directory (override via DMTCP_CHECKPOINT_DIR env var).
RUN mkdir -p /dmtcp/checkpoints
ENV DMTCP_CHECKPOINT_DIR=/dmtcp/checkpoints

# Volume that initContainers use to copy binaries to application pods.
VOLUME ["/share"]

# Default: start the coordinator on port 7779.
EXPOSE 7779
CMD ["/usr/bin/dmtcp_coordinator", "--port", "7779", "--background", "--coord-logfile", "/dmtcp/checkpoints/coordinator.log"]

