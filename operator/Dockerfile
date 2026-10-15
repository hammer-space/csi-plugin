# Development build. Pin base-image digests and complete container certification
# before producing a release bundle.
FROM registry.access.redhat.com/ubi9/go-toolset:1.25 AS builder
WORKDIR /opt/app-root/src/operator
COPY --chown=1001:0 operator/go.mod operator/go.sum ./
RUN go mod download
COPY --chown=1001:0 operator/ ./
COPY --chown=1001:0 deploy/kubernetes/kubernetes-1.36/plugin.yaml ../deploy/kubernetes/kubernetes-1.36/plugin.yaml
RUN go run ./cmd/generate-operands
RUN CGO_ENABLED=0 go build -trimpath -o /tmp/manager ./cmd

FROM registry.access.redhat.com/ubi9/ubi-micro:9.8@sha256:ff30602252a3e92706585c7c34cd3c74028eaac4ab815e6b19f8b27562bdd7e3
ARG VERSION=0.1.1
ARG RELEASE=1
LABEL name="hammerspace-csi-operator" \
      maintainer="Hammerspace" \
      vendor="Hammerspace" \
      version="${VERSION}" \
      release="${RELEASE}" \
      summary="Hammerspace CSI driver lifecycle management" \
      description="Manages Hammerspace CSI controller and node workloads for NFS and file-backed block storage."
COPY --from=builder /tmp/manager /usr/local/bin/manager
COPY operator/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/manager"]
