{{/*
Cross-field validations that values.schema.json cannot express cleanly
(conditional-required and mutual-exclusion relations between sibling keys).

Not yet included by any template (that lands in Task 7's secret.yaml), so its
fail path is not exercised until then.
*/}}
{{- define "hammerspace-csi.validateSecret" -}}
{{- if and .Values.hammerspace.secret.create (or (empty .Values.hammerspace.secret.username) (empty .Values.hammerspace.secret.password)) -}}
{{- fail "hammerspace.secret.create=true requires hammerspace.secret.username and .password" -}}
{{- end -}}
{{- end -}}
