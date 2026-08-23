#!/usr/bin/env bash
#
# validate.sh — offline kubeconform schema validation + trivy config
# misconfiguration scanning for the hammerspace-csi chart.
#
# Runs entirely offline (no live cluster, no network) against pre-warmed
# caches. Cache locations are overridable via env for CI portability:
#   KUBECONFORM_CACHE (default /tmp/kubeconform-cache)
#   TRIVY_CACHE        (default /tmp/trivycache)
#
# For each charts/hammerspace-csi/ci/*-values.yaml preset:
#   1. Render the chart (namespace kube-system).
#   2. kubeconform -strict against every target K8s version — any
#      schema-invalid resource fails this script (set -e).
#   3. trivy config scan (HIGH/CRITICAL) — printed for visibility but does
#      NOT fail the script here; this CSI node driver is intrinsically
#      privileged/hostNetwork by design. Regression is caught below.
#
# Regression gate (fails the script):
#   Render ci/default-values.yaml, run `trivy config -f json`, extract the
#   sorted unique set of HIGH/CRITICAL misconfiguration check IDs, and
#   compare against the checked-in tests/trivy-baseline.txt allowlist. Any
#   id that appears in the render but is NOT already in the baseline is
#   treated as a new regression and fails the script. Pre-existing baseline
#   ids that no longer appear are fine (chart got stricter).
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$(cd "$DIR/.." && pwd)"
BASELINE="$DIR/trivy-baseline.txt"

KUBECONFORM_CACHE="${KUBECONFORM_CACHE:-/tmp/kubeconform-cache}"
TRIVY_CACHE="${TRIVY_CACHE:-/tmp/trivycache}"
K8S_VERSIONS=(1.29.0 1.34.0 1.35.0 1.36.0)

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

render() {
  # render <values-file> <out-file>
  helm template hs "$CHART" -f "$1" --namespace kube-system > "$2"
}

for v in "$CHART"/ci/*-values.yaml; do
  name="$(basename "$v" -values.yaml)"
  rendered="$WORKDIR/${name}-rendered.yaml"
  echo "== validate $v =="
  render "$v" "$rendered"

  for kv in "${K8S_VERSIONS[@]}"; do
    echo "-- kubeconform strict (kubernetes $kv) --"
    kubeconform -strict -ignore-missing-schemas -kubernetes-version "$kv" \
      -cache "$KUBECONFORM_CACHE" -summary "$rendered"
  done

  echo "-- trivy config (informational, HIGH/CRITICAL) --"
  trivy config --cache-dir "$TRIVY_CACHE" --skip-check-update -q \
    --severity HIGH,CRITICAL "$rendered" || true
done

echo "== regression gate: trivy config baseline vs default render =="
if [[ ! -f "$BASELINE" ]]; then
  echo "FAIL: baseline file missing: $BASELINE" >&2
  echo "  Generate it from a real default-render scan (see tests/trivy-baseline.txt header)." >&2
  exit 1
fi

default_rendered="$WORKDIR/default-rendered.yaml"
render "$CHART/ci/default-values.yaml" "$default_rendered"

default_json="$WORKDIR/trivy-default.json"
trivy config --cache-dir "$TRIVY_CACHE" --skip-check-update -q \
  --severity HIGH,CRITICAL --format json "$default_rendered" > "$default_json"

if command -v jq >/dev/null 2>&1; then
  ids_found="$(jq -r '.Results[]?.Misconfigurations[]?.ID // empty' "$default_json" | sort -u)"
else
  ids_found="$(grep -oE '"ID":"[^"]+"' "$default_json" | sed -E 's/"ID":"([^"]+)"/\1/' | sort -u)"
fi

baseline_ids="$(grep -vE '^[[:space:]]*(#|$)' "$BASELINE" | awk '{print $1}' | sort -u)"

new_ids=""
if [[ -n "$ids_found" ]]; then
  new_ids="$(comm -23 <(printf '%s\n' "$ids_found") <(printf '%s\n' "$baseline_ids"))"
fi

if [[ -n "$new_ids" ]]; then
  echo "FAIL: new trivy config HIGH/CRITICAL id(s) not present in $BASELINE:" >&2
  echo "$new_ids" >&2
  echo "  If this is a genuine new by-design finding, add it to the baseline with justification." >&2
  echo "  If it is a real regression, fix the chart instead of widening the baseline." >&2
  exit 1
fi

found_count=0
if [[ -n "$ids_found" ]]; then
  found_count="$(printf '%s\n' "$ids_found" | wc -l | tr -d ' ')"
fi
echo "regression gate: OK ($found_count id(s) found in default render, all present in baseline)"

echo "validate: kubeconform strict + trivy config scan complete for all ci/*-values.yaml presets"
