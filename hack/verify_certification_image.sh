#!/usr/bin/env bash
# Verify locally observable Red Hat container-certification requirements.
# Red Hat preflight and the Certification Service vulnerability scan remain
# mandatory for the exact image digest that is submitted.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 IMAGE" >&2
  exit 2
fi

image=$1
layers=$(docker inspect --format '{{len .RootFS.Layers}}' "$image")
if (( layers >= 40 )); then
  echo "Image has $layers layers; certification requires fewer than 40" >&2
  exit 1
fi

for label in name maintainer vendor version release summary description; do
  value=$(docker inspect --format "{{ index .Config.Labels \"$label\" }}" "$image")
  if [[ -z "$value" || "$value" == "<no value>" ]]; then
    echo "Missing required image label: $label" >&2
    exit 1
  fi
done

docker run --rm --entrypoint sh "$image" -eu -c '
  test -d /licenses
  find -L /licenses -type f -print -quit | grep -q .
  test -n "$(rpm -q --whatprovides system-release 2>/dev/null)"
  if rpm -qa | grep -Eq "^(kernel|kernel-core|kernel-modules)(-|$)"; then
    echo "Image contains a prohibited kernel package" >&2
    exit 1
  fi
  non_redhat_rpms=$(rpm -qa --qf "%{NAME}|%{VENDOR}\n" | grep -v "|Red Hat, Inc.$" | grep -v "^gpg-pubkey|" || true)
  if [ -n "$non_redhat_rpms" ]; then
    echo "Third-party RPM provenance (include in certification review):"
    echo "$non_redhat_rpms"
  fi
'

echo "Local certification checks passed for $image ($layers layers)."
echo "Still required: preflight check container and Red Hat vulnerability scan for the pushed digest."
