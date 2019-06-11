# Copyright 2019 Hammerspace

FROM golang:1.10-alpine3.8
RUN apk add --no-cache git make py-pip
RUN pip install hstk
WORKDIR /go/src/github.com/hammer-space/csi-plugin/
ADD . ./
RUN go get golang.org/x/vgo
RUN make clean compile

FROM alpine:3.8
# Install required packages
RUN apk add --no-cache nfs-utils qemu-img ca-certificates xfsprogs e2fsprogs zfs btrfs-progs py-pip
RUN pip install hstk
WORKDIR /bin/
# Copy plugin binary from first stage
COPY --from=0 /go/src/github.com/hammer-space/csi-plugin/bin/hs-csi-plugin .
ENTRYPOINT ["/bin/hs-csi-plugin"]
