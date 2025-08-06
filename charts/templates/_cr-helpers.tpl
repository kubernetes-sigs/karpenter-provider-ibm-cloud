{{/*
Custom Resource Helper Templates
*/}}

{{/*
Generate mode-specific labels
*/}}
{{- define "karpenter.cr.modeLabels" -}}
{{- if .Values.customResources.mode }}
karpenter.ibm.sh/mode: {{ .Values.customResources.mode }}
{{- if eq .Values.customResources.mode "iks" }}
karpenter.ibm.sh/cluster-id: {{ .Values.iksClusterID | required "IKS cluster ID is required when mode is 'iks'" }}
{{- else if eq .Values.customResources.mode "vpc" }}
karpenter.ibm.sh/vpc-id: {{ .Values.customResources.nodeClass.vpc.vpcId | required "VPC ID is required when mode is 'vpc'" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Generate discovery tags based on mode
*/}}
{{- define "karpenter.cr.discoveryTags" -}}
karpenter.sh/discovery: {{ include "karpenter.fullname" . | quote }}
{{- if eq .Values.customResources.mode "iks" }}
karpenter.ibm.sh/cluster-id: {{ .Values.iksClusterID | quote }}
{{- else if eq .Values.customResources.mode "vpc" }}
karpenter.ibm.sh/vpc-id: {{ .Values.customResources.nodeClass.vpc.vpcId | quote }}
{{- end }}
{{- end }}

{{/*
Generate common node requirements
*/}}
{{- define "karpenter.cr.commonRequirements" -}}
- key: "karpenter.sh/nodepool"
  operator: In
  values: [{{ .poolName | quote }}]
- key: "node.kubernetes.io/instance-type"
  operator: In
  values:
    {{- range .instanceTypes }}
    - {{ . | quote }}
    {{- end }}
- key: "karpenter.sh/capacity-type"
  operator: In
  values: [{{ .capacityType | quote }}]
- key: "kubernetes.io/arch"
  operator: In
  values: [{{ .architecture | quote }}]
{{- end }}

{{/*
Validate configuration based on mode
*/}}
{{- define "karpenter.cr.validate" -}}
{{- if .Values.customResources.enabled }}
  {{- if not .Values.customResources.mode }}
    {{- fail "customResources.mode is required when customResources.enabled is true" }}
  {{- end }}

  {{- if eq .Values.customResources.mode "iks" }}
    {{- if not .Values.iksClusterID }}
      {{- fail "iksClusterID is required when customResources.mode is 'iks'" }}
    {{- end }}
  {{- else if eq .Values.customResources.mode "vpc" }}
    {{- if not .Values.customResources.nodeClass.vpc.vpcId }}
      {{- fail "customResources.nodeClass.vpc.vpcId is required when customResources.mode is 'vpc'" }}
    {{- end }}
  {{- else }}
    {{- fail "customResources.mode must be either 'iks' or 'vpc'" }}
  {{- end }}

  {{- range $pool := .Values.customResources.nodePools }}
    {{- if $pool.enabled }}
      {{- if not $pool.name }}
        {{- fail "NodePool name is required for all enabled nodePools" }}
      {{- end }}
    {{- end }}
  {{- end }}
{{- end }}
{{- end }}

{{/*
Generate environment-specific configuration
*/}}
{{- define "karpenter.cr.environmentConfig" -}}
{{- if eq .Values.customResources.mode "iks" }}
# IKS-specific environment variables
- name: KARPENTER_IBM_MODE
  value: "iks"
- name: IKS_CLUSTER_ID
  value: {{ .Values.iksClusterID | quote }}
{{- else if eq .Values.customResources.mode "vpc" }}
# VPC-specific environment variables
- name: KARPENTER_IBM_MODE
  value: "vpc"
- name: VPC_ID
  value: {{ .Values.customResources.nodeClass.vpc.vpcId | quote }}
{{- end }}
{{- end }}

{{/*
Generate bootstrap configuration based on mode
*/}}
{{- define "karpenter.cr.bootstrapConfig" -}}
{{- if eq .Values.customResources.mode "iks" }}
- name: BOOTSTRAP_MODE
  value: "iks-api"
{{- else if eq .Values.customResources.mode "vpc" }}
- name: BOOTSTRAP_MODE
  value: {{ .Values.bootstrapMode | default "cloud-init" | quote }}
{{- end }}
{{- end }}
