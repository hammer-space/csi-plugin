# Copyright 2019 Hammerspace

FROM golang:1.12-alpine3.9
RUN apk add --no-cache git make py-pip
RUN pip install hstk
WORKDIR /go/src/github.com/hammer-space/csi-plugin/
ADD . ./
RUN make clean compile

FROM alpine:3.9
# Install required packages
RUN apk add --no-cache nfs-utils qemu-img ca-certificates xfsprogs e2fsprogs zfs btrfs-progs py-pip
RUN pip install hstk
WORKDIR /hs-csi-plugin/
# Copy plugin binary from first stage
COPY --from=0 /go/src/github.com/hammer-space/csi-plugin/bin/hs-csi-plugin .
COPY LICENSE .
COPY DEPENDENCY_LICENSES .
ENTRYPOINT ["/hs-csi-plugin/hs-csi-plugin"]
