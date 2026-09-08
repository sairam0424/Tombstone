{{/*
Expand the name of the chart, truncated to 63 characters.
*/}}
{{- define "tombstone.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "tombstone.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "tombstone.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "tombstone.labels" -}}
helm.sh/chart: {{ include "tombstone.chart" . }}
{{ include "tombstone.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels — used in Deployment.spec.selector and Service.spec.selector.
*/}}
{{- define "tombstone.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tombstone.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name — honours .Values.serviceAccount.name when set.
*/}}
{{- define "tombstone.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "tombstone.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
INFRA-1: pod-level securityContext, shared across every Deployment so a
future hardening change is made once, not five times. runAsNonRoot fails
the pod at admission if an image's own Dockerfile forgets a USER directive,
which is the point — catch it at deploy time, not via a runtime CVE.
*/}}
{{- define "tombstone.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/*
INFRA-1: the part of container securityContext that's safe to share
unconditionally (dropping capabilities and privilege escalation has no
functional effect on a normal HTTP server process). readOnlyRootFilesystem
is deliberately NOT included here — it's a real behavioral change that
could break a container that writes to disk anywhere outside a mounted
volume, and this repo has no cluster available to verify each service
against; it's set per-service in values.yaml instead (see
<service>.readOnlyRootFilesystem), defaulting true for the 4 Go services
and false for intelligence (bundles a large ML model that may write
runtime cache files — unverified, not guessed).
*/}}
{{- define "tombstone.containerSecurityContextBase" -}}
allowPrivilegeEscalation: false
capabilities:
  drop:
    - ALL
{{- end }}

{{/*
INFRA-1: soft (ScheduleAnyway, never blocks scheduling) spread of one
component's replicas across nodes and zones — the plan's own "anti-
affinity/topology spread" item. Soft specifically so a small/single-node
dev or staging cluster can still schedule every pod; a hard requirement
would make replicaCount>nodeCount unschedulable.
Usage: {{ include "tombstone.topologySpreadConstraints" (dict "component" "flag-api" "context" $) }}
*/}}
{{- define "tombstone.topologySpreadConstraints" -}}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- include "tombstone.selectorLabels" .context | nindent 8 }}
        app.kubernetes.io/component: {{ .component }}
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- include "tombstone.selectorLabels" .context | nindent 8 }}
        app.kubernetes.io/component: {{ .component }}
{{- end }}
