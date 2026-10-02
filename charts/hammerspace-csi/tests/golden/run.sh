#!/usr/bin/env bash
#
# run.sh — golden-manifest snapshot test.
#
# Proves the chart's DEFAULT render (ci/default-values.yaml, namespace
# kube-system) is behavior-equivalent to the shipped manifest
# deploy/kubernetes/kubernetes-1.36/plugin.yaml, which is captured pristine as
# tests/golden/expected-1.36.yaml.
#
# Both the render and the expected file are pushed through normalize.sh (see
# that script's header) which canonicalizes ordering/comments and strips ONLY
# the four cosmetic app.kubernetes.io/* + helm.sh/chart labels the chart adds.
# Any surviving difference is a REAL behavior drift and fails the gate.
#
# Exit 0 => identical.  Exit non-zero + "GOLDEN DRIFT" banner => drift.
set -euo pipefail

# Resolve paths relative to this script so it runs from any CWD.
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="$(cd "$DIR/../.." && pwd)"
NORM="$DIR/normalize.sh"
EXPECTED="$DIR/expected-1.36.yaml"

render() {
  helm template hs "$CHART" -f "$CHART/ci/default-values.yaml" --namespace kube-system \
    | bash "$NORM"
}
expected() {
  bash "$NORM" < "$EXPECTED"
}

if ! diff -u <(expected) <(render); then
  echo "" >&2
  echo "GOLDEN DRIFT: default chart render differs from tests/golden/expected-1.36.yaml" >&2
  echo "  The only tolerated difference is the cosmetic app.kubernetes.io/* + helm.sh/chart" >&2
  echo "  labels (already stripped by normalize.sh). Any diff above is a real behavior change." >&2
  echo "  If it is a deliberate, reviewed change to the shipped manifest, re-derive" >&2
  echo "  expected-1.36.yaml from deploy/kubernetes/kubernetes-1.36/plugin.yaml and record it." >&2
  exit 1
fi
echo "golden: default render matches expected-1.36 (behavior-equivalent to plugin.yaml)"
