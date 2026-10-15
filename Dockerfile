# Copyright 2019 Hammerspace

# Override only when using a separately maintained, entitled RHEL runtime base.
ARG RUNTIME_BASE=runtime

# ---------- Stage 1: Builder ----------
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder
USER 0
WORKDIR /go/src/github.com/hammer-space/csi-plugin
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY pkg ./pkg
ARG version=dev
ARG githash=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-X github.com/hammer-space/csi-plugin/pkg/common.Version=${version} -X github.com/hammer-space/csi-plugin/pkg/common.Githash=${githash}" \
    -o /tmp/hs-csi-plugin .

# ---------- Stage 2: Public runtime dependencies ----------
FROM registry.access.redhat.com/ubi9/ubi:9.6 AS runtime
# The public signing key verifies RPMs; it is not a subscription credential.
# The installer protects installed UBI packages and checks their file integrity.
COPY ubi/RPM-GPG-KEY-Rocky-9 /etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9
COPY ubi/storage-tools.repo /etc/yum.repos.d/hs-storage-tools.repo
COPY ubi/install-runtime-packages.sh /tmp/install-runtime-packages.sh
RUN bash /tmp/install-runtime-packages.sh && \
    rm /tmp/install-runtime-packages.sh && \
    python3 -m pip install --no-cache-dir --user hstk
ENV PATH=$PATH:/root/.local/bin
# RPM license files remain installed; expose them alongside application licenses.
RUN mkdir -p /licenses && ln -s /usr/share/licenses /licenses/rpm-packages

# ---------- Stage 3: Driver ----------
FROM ${RUNTIME_BASE}
ARG version=dev
ARG release=1
ARG githash=unknown
LABEL name="hammerspace-csi-plugin" \
      maintainer="Hammerspace" \
      vendor="Hammerspace" \
      version="${version}" \
      release="${release}" \
      summary="Hammerspace CSI driver" \
      description="Implements the Container Storage Interface for Hammerspace NFS and file-backed block volumes." \
      org.opencontainers.image.revision="${githash}"
ENV PATH=$PATH:/root/.local/bin
WORKDIR /hs-csi-plugin
COPY --from=builder /tmp/hs-csi-plugin ./hs-csi-plugin
COPY LICENSE DEPENDENCY_LICENSES /licenses/
# Mount, loop-device and filesystem operations require root and privileged pods.
USER 0
ENTRYPOINT ["/hs-csi-plugin/hs-csi-plugin"]
