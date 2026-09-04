{{/* vim: set filetype=mustache: */}}

{{/*
Expand the name of the chart.
*/}}
{{- define "rsync-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name (release name based).
We truncate at 63 chars because some Kubernetes name fields are limited to this.
*/}}
{{- define "rsync-gateway.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name and version for the helm.sh/chart label.
*/}}
{{- define "rsync-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "rsync-gateway.labels" -}}
helm.sh/chart: {{ include "rsync-gateway.chart" . }}
{{ include "rsync-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels (shared by resources and their pod templates / service selectors).
*/}}
{{- define "rsync-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rsync-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the service account used by the manager. Explicit rbac.serviceAccount.name
wins; when the chart manages the account it is the release fullname; otherwise
the workload falls back to the namespace's "default" service account.
*/}}
{{- define "rsync-gateway.serviceAccountName" -}}
{{- if .Values.rbac.serviceAccount.name -}}
{{- .Values.rbac.serviceAccount.name -}}
{{- else if .Values.rbac.serviceAccount.create -}}
{{ include "rsync-gateway.fullname" . }}
{{- else -}}
default
{{- end -}}
{{- end -}}

{{/*
Name of the manager ClusterRole (and its binding): explicit
rbac.clusterRole.name wins, otherwise <fullname>-manager.
*/}}
{{- define "rsync-gateway.clusterRoleName" -}}
{{- default (printf "%s-manager" (include "rsync-gateway.fullname" .)) .Values.rbac.clusterRole.name -}}
{{- end -}}

{{/*
Build the container image reference: repository[:tag].
An empty image.tag falls back to Chart.AppVersion.
*/}}
{{- define "rsync-gateway.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/*
Extract the TCP port from a listen address ("host:port", ":port",
"host:port" with an IPv6 literal like "[::1]:port"). Fails the rendering
when no port can be determined, so the container ports / Services never
silently diverge from the flags.
Usage: include "rsync-gateway.portFromAddr" ":873"
*/}}
{{- define "rsync-gateway.portFromAddr" -}}
{{- $addr := toString . -}}
{{- $parts := splitList ":" $addr -}}
{{- if or (lt (len $parts) 2) (eq (last $parts) "") -}}
{{- fail (printf "address %q carries no TCP port" $addr) -}}
{{- end -}}
{{- last $parts -}}
{{- end -}}
