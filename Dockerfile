# Copyright 2019 Hammerspace

FROM registry.access.redhat.com/ubi8/ubi:8.4
RUN dnf --disableplugin=subscription-manager -y install python2-pip git golang make

RUN pip2 install hstk
WORKDIR /go/src/github.com/hammer-space/csi-plugin/
ADD . ./
RUN make compile

FROM registry.access.redhat.com/ubi8/ubi:8.4
# Install required packages
ADD ubi/CentOS-Base.repo /etc/yum.repos.d/CentOS-Base.repo
ADD ubi/CentOS-AppStream.repo /etc/yum.repos.d/CentOS-AppStream.repo
ADD ubi/RPM-GPG-KEY-centosofficial /etc/pki/rpm-gpg/RPM-GPG-KEY-centosofficial
RUN dnf --disableplugin=subscription-manager --nobest -y install python2-pip libcom_err-devel \
	ca-certificates-2021.2.50-80.0.el8_4.noarch \
	e2fsprogs-1.45.6-2.el8.x86_64 \
	#-1.45.6-1.el8.x86_64 \
	e2fsprogs-libs-1.45.6-2.el8.x86_64 \
	#-1.45.6-1.el8.x86_64 \
	gssproxy-0.8.0-19.el8.x86_64 \
	#-0.8.0-16.el8.x86_64 \
	keyutils-libs-1.5.10-9.el8.x86_64 \
	#-1.5.10-6.el8.x86_64 \
	keyutils-1.5.10-9.el8.x86_64 \
	#-1.5.10-6.el8.x86_64 \
	libbasicobjects-0.1.1-39.el8.x86_64 \
	libcollection-0.7.0-39.el8.x86_64 \
	libini_config-1.3.1-39.el8.x86_64 \
	libnfsidmap-2.3.3-46.el8.x86_64 \
	#-2.3.3-35.el8.x86_64 \
	nfs-utils-2.3.3-46.el8.x86_64 \
	#-2.3.3-35.el8.x86_64 \
	libref_array-0.1.5-39.el8.x86_64 \
	libverto-libevent-0.3.0-5.el8.x86_64 \
	qemu-img-4.2.0-59.module_el8.5.0+1002+36725df2.x86_64 \
	#-4.2.0-34.module_el8.3.0+613+9ec9f184.1.x86_64 \
	quota-4.04-14.el8.x86_64 \
	#-4.04-10.el8.x86_64 \
	quota-nls-4.04-14.el8.noarch \
	#-4.04-10.el8.noarch \
	rpcbind-1.2.5-8.el8.x86_64 \
	#-1.2.5-7.el8.x86_64 \
	xfsprogs-5.0.0-9.el8.x86_64
	#-5.0.0-4.el8.x86_64 &&
#dnf clean all

# zfs btrfs-progs py-pip
RUN pip2 install hstk
WORKDIR /hs-csi-plugin/
# Copy plugin binary from first stage
COPY --from=0 /go/src/github.com/hammer-space/csi-plugin/bin/hs-csi-plugin .
COPY LICENSE .
COPY DEPENDENCY_LICENSES .
ENTRYPOINT ["/hs-csi-plugin/hs-csi-plugin"]
