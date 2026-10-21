{{- define "om.fullname" -}}
{{- printf "%s-openmeter" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* A component's name: <release>-openmeter-<component>. */}}
{{- define "om.name" -}}
{{- printf "%s-%s" (include "om.fullname" .root) .component | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "om.labels" -}}
app.kubernetes.io/name: nebari-openmeter
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "om.selector" -}}
app.kubernetes.io/name: nebari-openmeter
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "om.consoleImage" -}}
{{- $i := .Values.console.image -}}
{{- if $i.digest -}}
{{ $i.repository }}{{ with $i.tag }}:{{ . }}{{ end }}@{{ $i.digest }}
{{- else -}}
{{ $i.repository }}:{{ required "console.image.tag or console.image.digest is required" $i.tag }}
{{- end -}}
{{- end -}}

{{/* The env every OpenMeter process needs for its stores' passwords. */}}
{{- define "om.storeEnv" -}}
- name: POSTGRES_URL
  valueFrom: {secretKeyRef: {name: {{ include "om.name" (dict "root" . "component" "credentials") }}, key: postgres-url}}
- name: AGGREGATION_CLICKHOUSE_PASSWORD
  valueFrom: {secretKeyRef: {name: {{ include "om.name" (dict "root" . "component" "credentials") }}, key: clickhouse-password}}
{{- end -}}

{{- define "om.securityContext" -}}
allowPrivilegeEscalation: false
capabilities: {drop: [ALL]}
{{- end -}}

{{/* A store's data volume: its claim when persistence is on, else emptyDir. */}}
{{- define "om.dataClaim" -}}
{{- if .store.persistence.enabled }}
volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ReadWriteOnce]
      {{- with .store.persistence.storageClass }}
      storageClassName: {{ . }}
      {{- end }}
      resources:
        requests:
          storage: {{ .store.persistence.size }}
{{- end }}
{{- end -}}
