{{- define "om.fullname" -}}
{{- printf "%s-openmeter" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
A component's name: <release>-openmeter-<component>, at most 50 characters.
A long release name is cut, never the component, so a name that ends in a
hash (the price-sync Job) keeps all of it. 50 is the tightest limit among
the components: CloudNativePG refuses a Cluster name over 50, and a
StatefulSet over 52 cannot create pods (their controller-revision-hash label
is the name plus a hash and must fit 63).
*/}}
{{- define "om.name" -}}
{{- $prefix := include "om.fullname" .root | trunc (int (sub 49 (len .component))) | trimSuffix "-" -}}
{{- printf "%s-%s" $prefix .component -}}
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

{{/* The usage console's image (usage-console/). */}}
{{- define "om.consoleImage" -}}
{{- $i := .Values.console.image -}}
{{- if $i.digest -}}
{{ $i.repository }}{{ with $i.tag }}:{{ . }}{{ end }}@{{ $i.digest }}
{{- else -}}
{{ $i.repository }}:{{ required "console.image.tag or console.image.digest is required" $i.tag }}
{{- end -}}
{{- end -}}

{{/* The openmeter-setup image the bootstrap and ensure-secret Jobs run. */}}
{{- define "om.setupImage" -}}
{{- $i := .Values.setup.image -}}
{{- if $i.digest -}}
{{ $i.repository }}{{ with $i.tag }}:{{ . }}{{ end }}@{{ $i.digest }}
{{- else -}}
{{ $i.repository }}:{{ required "setup.image.tag or setup.image.digest is required" $i.tag }}
{{- end -}}
{{- end -}}

{{/* The Secret with ClickHouse's password (key clickhouse-password). */}}
{{- define "om.clickhouseSecret" -}}
{{- .Values.clickhouse.existingSecret | default (include "om.name" (dict "root" . "component" "clickhouse-password")) -}}
{{- end -}}

{{/* The env every OpenMeter process needs for its stores' passwords. */}}
{{- define "om.storeEnv" -}}
- name: POSTGRES_URL
  valueFrom: {secretKeyRef: {name: {{ printf "%s-app" (include "om.name" (dict "root" . "component" "db")) }}, key: uri}}
- name: AGGREGATION_CLICKHOUSE_PASSWORD
  valueFrom: {secretKeyRef: {name: {{ include "om.clickhouseSecret" . }}, key: clickhouse-password}}
{{- end -}}

{{- define "om.securityContext" -}}
allowPrivilegeEscalation: false
capabilities: {drop: [ALL]}
{{- end -}}

{{/*
A store's data volume: its claim when persistence is on, else emptyDir. The
claim outlives the StatefulSet (helm uninstall) unless keepDataOnUninstall
is false.
*/}}
{{- define "om.dataClaim" -}}
{{- if .store.persistence.enabled }}
persistentVolumeClaimRetentionPolicy:
  whenDeleted: {{ ternary "Retain" "Delete" .root.Values.keepDataOnUninstall }}
  whenScaled: Retain
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
