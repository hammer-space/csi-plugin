# Copyright 2019-2025 Hammerspace, Inc.
# ----------------------------------------------------------------------------
# Dockerfile for Hammerspace CSI Plugin
# ----------------------------------------------------------------------------
# This Dockerfile builds the Hammerspace CSI Plugin for use with Kubernetes.
# The plugin is built in a multi-stage build process to reduce the size of the final image.
# The first stage builds the plugin binary using Go, and the second stage creates a minimal image
# with the binary and its dependencies.
# The final image is based on Red Hat Universal Base Image (UBI) 8.4.
# The image is designed to be used in a Kubernetes cluster with Hammerspace.
# The plugin is built using Go 1.24 and requires the following dependencies:
# - python3-pip
# - libcom_err-devel
# - ca-certificates
# - e2fsprogs
# - gssproxy
# - keyutils-libs
# - libbasicobjects
# - libcollection
# - libini_config
# - libnfsidmap
# - nfs-utils
# - libref_array
# - libverto-libevent
# - qemu-img
# - quota
# - rpcbind
# - xfsprogs
# The image is designed to be used with Kubernetes 1.18 or later with Hammerspace.

# git                          x86_64  2.43.5-2.el8_10                         ubi-8-appstream   92 k
# golang                       x86_64  1.23.6-1.module+el8.10.0+22945+b2c96a17 ubi-8-appstream  762 k
# make                         x86_64  1:4.2.1-11.el8                          ubi-8-baseos     498 k
# python3-pip                  noarch  9.0.3-24.el8                            ubi-8-appstream   20 k

# ----------------------------------------------------------------------------
# Dockerfile for Hammerspace CSI Plugin
# ----------------------------------------------------------------------------

# ---------- Stage 1: Build ----------
# Install build tools
FROM registry.access.redhat.com/ubi8/ubi:8.4 AS builder

RUN dnf --disableplugin=subscription-manager -y install python3-pip git golang make \
    && dnf clean all && rm -rf /var/cache/dnf /var/cache/yum

# Install hstk using Python 3
RUN python3 -m pip install --no-cache-dir --user hstk
ENV PATH=$PATH:/root/.local/bin

WORKDIR /go/src/github.com/hammer-space/csi-plugin/
ADD . ./
RUN make compile

# ---------- Stage 2: Runtime ----------
FROM registry.access.redhat.com/ubi8/ubi:8.4

# Optional: Add custom repos for UBI compatibility
ADD ubi/CentOS-Base.repo /etc/yum.repos.d/CentOS-Base.repo
ADD ubi/CentOS-AppStream.repo /etc/yum.repos.d/CentOS-AppStream.repo
ADD ubi/RPM-GPG-KEY-centosofficial /etc/pki/rpm-gpg/RPM-GPG-KEY-centosofficial

# Install runtime dependencies
# - e2fsprogs-1.45.6-2.el8.x86_64 (prev: 1.45.6-1.el8)
# - gssproxy-0.8.0-19.el8 (prev: 0.8.0-16.el8)
# - keyutils-libs-1.5.10-9.el8 (prev: 1.5.10-6.el8)
# - qemu-img-4.2.0-59.module_el8.5.0 (prev: 4.2.0-34.el8.3)
# - quota-4.04-14.el8 (prev: 4.04-10.el8)
RUN dnf --disableplugin=subscription-manager --nobest -y install \
    python3-pip libcom_err-devel \
    ca-certificates-2021.2.50-80.0.el8_4.noarch \
    e2fsprogs-1.45.6-2.el8.x86_64 \
    e2fsprogs-libs-1.45.6-2.el8.x86_64 \
    gssproxy-0.8.0-19.el8.x86_64 \
    keyutils-libs-1.5.10-9.el8.x86_64 \
    keyutils-1.5.10-9.el8.x86_64 \
    libbasicobjects-0.1.1-39.el8.x86_64 \
    libcollection-0.7.0-39.el8.x86_64 \
    libini_config-1.3.1-39.el8.x86_64 \
    libnfsidmap-2.3.3-46.el8.x86_64 \
    nfs-utils-2.3.3-46.el8.x86_64 \
    libref_array-0.1.5-39.el8.x86_64 \
    libverto-libevent-0.3.0-5.el8.x86_64 \
    qemu-img-4.2.0-59.module_el8.5.0+1002+36725df2.x86_64 \
    quota-4.04-14.el8.x86_64 \
    quota-nls-4.04-14.el8.noarch \
    rpcbind-1.2.5-8.el8.x86_64 \
    xfsprogs-5.0.0-9.el8.x86_64 \
 && dnf clean all && rm -rf /var/cache/dnf


# Set working directory
WORKDIR /hs-csi-plugin/

# Copy binary from builder
COPY --from=builder /go/src/github.com/hammer-space/csi-plugin/bin/hs-csi-plugin .

# Include license files
COPY LICENSE .
COPY DEPENDENCY_LICENSES .

# Set entrypoint
ENTRYPOINT ["/hs-csi-plugin/hs-csi-plugin"]
