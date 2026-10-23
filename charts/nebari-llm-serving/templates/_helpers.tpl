{{/*
Expand the name of the chart.
*/}}
{{- define "nebari-llm-serving.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "nebari-llm-serving.fullname" -}}
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
{{- define "nebari-llm-serving.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nebari-llm-serving.labels" -}}
helm.sh/chart: {{ include "nebari-llm-serving.chart" . }}
{{ include "nebari-llm-serving.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nebari-llm-serving.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nebari-llm-serving.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Operator namespace (the namespace where the operator and key manager run)
*/}}
{{/*
OpenMeter's API: usage.openmeter.url, else the bundled OpenMeter when
openmeter.enabled.
*/}}
{{- define "nebari-llm-serving.openmeterURL" -}}
{{- if .Values.usage.openmeter.url -}}
{{ .Values.usage.openmeter.url }}
{{- else if .Values.openmeter.enabled -}}
{{ printf "http://%s-openmeter-api.%s.svc" .Release.Name .Release.Namespace }}
{{- else -}}
{{ fail "usage.enabled=true needs usage.openmeter.url, or openmeter.enabled=true for the bundled OpenMeter" }}
{{- end -}}
{{- end }}

{{- define "nebari-llm-serving.operatorNamespace" -}}
{{- printf "%s-system" (include "nebari-llm-serving.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
