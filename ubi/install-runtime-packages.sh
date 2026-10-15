#!/usr/bin/env bash
# Add signed storage tools without replacing any installed UBI package.
set -euo pipefail

ubi_repos=(--disablerepo='*' --enablerepo=ubi-9-baseos-rpms --enablerepo=ubi-9-appstream-rpms)
dnf_options=(--disableplugin=subscription-manager -y --setopt=install_weak_deps=False)

# Resolve updates and common dependencies from Red Hat before enabling Rocky.
dnf "${dnf_options[@]}" "${ubi_repos[@]}" upgrade
dnf "${dnf_options[@]}" "${ubi_repos[@]}" install util-linux python3-pip ca-certificates

mkdir -p /usr/share/hs-csi-plugin
rpm_format='%{NAME} %{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH} %{VENDOR}\n'
rpm -qa --qf "$rpm_format" | sort > /tmp/ubi-packages.before
protected_packages=$(rpm -qa --qf '%{NAME},')
rpm -qa --qf '%{NAME}\n' > /tmp/ubi-package-names

verify_ubi_files() {
    local status=0
    # rpm returns 1 for file differences; record existing differences in the
    # upstream image so only newly changed non-configuration files fail us.
    rpm -V --noconfig $(cat /tmp/ubi-package-names) > "$1.unsorted" || status=$?
    if (( status > 1 )); then
        return "$status"
    fi
    sort "$1.unsorted" > "$1"
    rm "$1.unsorted"
}
verify_ubi_files /tmp/ubi-files.before

# Repository priorities prefer UBI for new dependencies too. Exclusions prevent
# Rocky from upgrading/replacing installed packages even if versions diverge.
# An unsatisfied dependency must fail the build, never replace UBI content.
dnf "${dnf_options[@]}" "${ubi_repos[@]}" \
    --setopt=ubi-9-baseos-rpms.priority=10 \
    --setopt=ubi-9-appstream-rpms.priority=10 \
    --enablerepo=hs-storage-baseos --enablerepo=hs-storage-appstream \
    --setopt="hs-storage-baseos.excludepkgs=${protected_packages}" \
    --setopt="hs-storage-appstream.excludepkgs=${protected_packages}" \
    install nfs-utils e2fsprogs xfsprogs qemu-img

rpm -qa --qf "$rpm_format" | sort > /usr/share/hs-csi-plugin/runtime-packages.txt
comm -23 /tmp/ubi-packages.before /usr/share/hs-csi-plugin/runtime-packages.txt > /tmp/ubi-packages.changed
if [[ -s /tmp/ubi-packages.changed ]]; then
    echo 'Storage package installation replaced UBI packages:' >&2
    cat /tmp/ubi-packages.changed >&2
    exit 1
fi
verify_ubi_files /tmp/ubi-files.after
comm -13 /tmp/ubi-files.before /tmp/ubi-files.after > /tmp/ubi-files.changed
if [[ -s /tmp/ubi-files.changed ]]; then
    echo 'Storage package installation modified UBI files:' >&2
    cat /tmp/ubi-files.changed >&2
    exit 1
fi

# Keep third-party repositories disabled for subsequent consumers of this image.
dnf --disableplugin=subscription-manager clean all
rm -rf /var/cache/dnf /tmp/ubi-packages.* /tmp/ubi-package-names /tmp/ubi-files.*
