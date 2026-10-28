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
{{- define "nebari-llm-serving.operatorNamespace" -}}
{{- printf "%s-system" (include "nebari-llm-serving.fullname" .) | trunc 63 | trimSuffix "-" }}
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
The bundled OpenMeter's namespace: openmeter.namespace, which the subchart
installs into. Required: a subchart cannot be handed the pack's computed
namespace, and the release namespace is wherever helm happened to run. It
must not be the pack's namespace: the subchart's pre-install hook would run
before the chart creates that namespace, uninstalling would delete the data
keepDataOnUninstall keeps, and the key-manager can read every Secret there
(#215), OpenMeter's credentials included.
*/}}
{{- define "nebari-llm-serving.openmeterNamespace" -}}
{{- $pack := include "nebari-llm-serving.operatorNamespace" . -}}
{{- if not .Values.openmeter.namespace -}}
{{- fail (printf "openmeter.enabled needs openmeter.namespace: a namespace that exists, other than the pack's namespace (%s)" $pack) -}}
{{- end -}}
{{- if eq .Values.openmeter.namespace $pack -}}
{{- fail (printf "openmeter.namespace must not be the pack's namespace (%s): create another namespace for OpenMeter, such as usage-openmeter" $pack) -}}
{{- end -}}
{{- .Values.openmeter.namespace -}}
{{- end }}
