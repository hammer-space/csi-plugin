#!/usr/bin/env bash
#
# normalize.sh — canonicalize a multi-doc Kubernetes YAML stream (read on stdin,
# written to stdout) so that cosmetic differences never produce a false golden diff.
#
# Canonicalization performed (in order):
#   1. Strip ALL comments (head/line/foot) from every node.
#   2. Collect every document into a list and drop null / empty documents
#      (e.g. a document that was nothing but comments).
#   3. Strip ONLY the four cosmetic labels that Helm/this chart adds to each
#      resource's top-level `.metadata.labels`:
#         app.kubernetes.io/name
#         app.kubernetes.io/instance
#         app.kubernetes.io/managed-by
#         helm.sh/chart
#      If that leaves `.metadata.labels` empty, the (now-empty) labels map is
#      removed so a resource that had NO labels in the source manifest still
#      compares equal.
#      This is the ONE tolerated difference between the chart render and the
#      shipped 1.36 manifest (see tests/golden/run.sh and expected-1.36.yaml).
#      NOTHING ELSE is stripped: functional labels such as `app: csi-provisioner`
#      / `app: csi-node`, every `spec.selector`, `matchLabels`, and pod-template
#      label are left untouched — so a genuinely dropped/changed functional label
#      still surfaces as drift.
#   4. Sort map keys recursively (key order is not significant).
#   5. Sort the documents by `kind` + "/" + `metadata.name` (document order is
#      not significant).
#   6. Re-emit as a `---`-separated multi-document stream.
#
# The transform is deterministic and idempotent: normalize(normalize(x)) == normalize(x).
set -euo pipefail

yq ea '
  ... comments="" |
  [ . ] |
  map( select( . != null ) ) |
  map(
    del(.metadata.labels."app.kubernetes.io/name",
        .metadata.labels."app.kubernetes.io/instance",
        .metadata.labels."app.kubernetes.io/managed-by",
        .metadata.labels."helm.sh/chart")
    | ( select(.metadata.labels != null and (.metadata.labels | length == 0)).metadata |= del(.labels) )
  ) |
  sort_by( (.kind // "") + "/" + (.metadata.name // "") ) |
  sort_keys(..) |
  .[] | split_doc
' -
