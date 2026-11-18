{{- define "karpenter.cr.validate" -}}
{{- if not (has .Values.bootstrapMode (list "auto" "cloud-init" "iks-api")) }}
  {{- fail "bootstrapMode must be auto, cloud-init or iks-api" }}
{{- end }}
{{- if .Values.customResources.enabled }}
  {{- if not (has .Values.customResources.mode (list "vpc" "iks")) }}
    {{- fail "customResources.mode must be vpc or iks" }}
  {{- end }}
  {{- range $key, $_ := .Values.customResources.nodeClass }}
    {{- if not (has $key (list "enabled" "name" "spec")) }}
      {{- fail (printf "customResources.nodeClass.%s is unsupported; migrate to nodeClass.spec (charts/examples/)" $key) }}
    {{- end }}
  {{- end }}
  {{- if .Values.customResources.nodeClass.enabled }}
    {{- $spec := .Values.customResources.nodeClass.spec }}
    {{- range $key := list "region" "vpc" "resourceGroup" "apiServerEndpoint" }}
      {{- if not (get $spec $key) }}
        {{- fail (printf "customResources.nodeClass.spec.%s is required" $key) }}
      {{- end }}
    {{- end }}
    {{- $mode := get $spec "bootstrapMode" | default "auto" }}
    {{- $iks := or (eq $mode "iks-api") (and (ne $mode "cloud-init") (get $spec "iksClusterID")) }}
    {{- if and (eq .Values.customResources.mode "iks") (not $iks) }}
      {{- fail "IKS NodeClass must set spec.bootstrapMode=iks-api and spec.iksClusterID" }}
    {{- end }}
    {{- if and (eq .Values.customResources.mode "vpc") $iks }}
      {{- fail "VPC NodeClass cannot select IKS bootstrap" }}
    {{- end }}
  {{- end }}
  {{- range $pool := .Values.customResources.nodePools }}
    {{- if and $pool.enabled (not $pool.name) }}
      {{- fail "NodePool name is required for enabled pools" }}
    {{- end }}
  {{- end }}
{{- end }}
{{- end }}
