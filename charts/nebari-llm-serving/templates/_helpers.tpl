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
The pack's namespace, where the operator and key manager run:
global.namespace, else <release>-nebari-llm-serving-system, or
<release>-system when the release name contains nebari-llm-serving. It reads
only .Release and .Values.global, which every chart in the release sees, so
the bundled OpenMeter subchart calls this same helper. Not derived from
fullnameOverride or nameOverride: those name resources, not the namespace.
*/}}
{{- define "nebari-llm-serving.operatorNamespace" -}}
{{- with (.Values.global).namespace -}}
{{ . }}
{{- else -}}
{{- $full := .Release.Name -}}
{{- if not (contains "nebari-llm-serving" .Release.Name) -}}
{{- $full = printf "%s-nebari-llm-serving" .Release.Name -}}
{{- end -}}
{{- printf "%s-system" ($full | trunc 63 | trimSuffix "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
OpenMeter's API: usage.openmeter.url, else the bundled OpenMeter when
openmeter.enabled.
*/}}
{{- define "nebari-llm-serving.openmeterURL" -}}
{{- if .Values.usage.openmeter.url -}}
{{ .Values.usage.openmeter.url }}
{{- else if .Values.openmeter.enabled -}}
{{ printf "http://%s.%s.svc" (include "om.name" (dict "root" . "component" "api")) (include "nebari-llm-serving.openmeterNamespace" .) }}
{{- else -}}
{{ fail "usage.enabled=true needs usage.openmeter.url, or openmeter.enabled=true for the bundled OpenMeter" }}
{{- end -}}
{{- end }}

{{/*
The bundled OpenMeter's namespace: openmeter.namespace, else the pack's.
*/}}
{{- define "nebari-llm-serving.openmeterNamespace" -}}
{{- .Values.openmeter.namespace | default (include "nebari-llm-serving.operatorNamespace" .) -}}
{{- end }}

{{/*
Whether the bundled OpenMeter is in the namespace this chart creates.
Uninstalling deletes that namespace, so keepDataOnUninstall can't keep
anything there (NOTES.txt says so).
*/}}
{{- define "nebari-llm-serving.openmeterInChartNamespace" -}}
{{- if and .Values.openmeter.enabled .Values.createNamespace (eq (include "nebari-llm-serving.openmeterNamespace" .) (include "nebari-llm-serving.operatorNamespace" .)) -}}
true
{{- end -}}
{{- end }}
