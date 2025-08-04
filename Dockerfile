# Copyright 2019 Hammerspace

# ---------- Stage 1: Builder ----------
FROM rockylinux/rockylinux:9-ubi AS builder

# Install build tools
RUN dnf -y update && \
    dnf -y install python3-pip git golang make && \
    dnf clean all

# Install hstk (Python 3 version)
RUN python3 -m pip install --no-cache-dir --user hstk
ENV PATH=$PATH:/root/.local/bin

# Set working directory
WORKDIR /go/src/github.com/hammer-space/csi-plugin/

# Add source code
ADD . ./

# Build plugin
RUN make compile

# ---------- Stage 2: Runtime ----------
FROM rockylinux/rockylinux:9-ubi

# Enable `devel` repo to access libverto-libevent
RUN dnf --nodocs --nobest -y install \
    dnf-plugins-core && \
    dnf config-manager --set-enabled devel && \
    dnf -y update && \
    dnf -y install \
        util-linux \
        python3-pip \
        libcom_err-devel \
        ca-certificates \
        e2fsprogs \
        e2fsprogs-libs \
        gssproxy \
        keyutils-libs \
        keyutils \
        libbasicobjects \
        libcollection \
        libini_config \
        libnfsidmap \
        nfs-utils \
        libref_array \
        libverto-libevent \
        qemu-img \
        quota \
        quota-nls \
        rpcbind \
        xfsprogs && \
    dnf clean all && \
    rm -rf /var/cache/dnf

# Install hstk using pip3
RUN python3 -m pip install --no-cache-dir --user hstk
ENV PATH=$PATH:/root/.local/bin

# Set working directory
WORKDIR /hs-csi-plugin/

# Copy CSI plugin binary from build stage
COPY --from=builder /go/src/github.com/hammer-space/csi-plugin/bin/hs-csi-plugin .

# Include license files
COPY LICENSE .
COPY DEPENDENCY_LICENSES .
# Set entrypoint to the plugin binary
ENTRYPOINT ["/hs-csi-plugin/hs-csi-plugin"]
