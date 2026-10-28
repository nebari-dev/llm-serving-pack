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

{{/*
The namespace every resource of the subchart goes in: namespace, or the
release namespace when it is empty.
*/}}
{{- define "om.namespace" -}}
{{- .Values.namespace | default .Release.Namespace -}}
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

{{/*
The console's token check: OIDC_ISSUER_URL and OIDC_CLIENT_ID from
console.oidc, or else, when the NebariApp's gateway sign-in forwards access
tokens, from the Secret the Nebari operator makes for the console's Keycloak
client. Nothing (dev mode) otherwise.
*/}}
{{- define "om.consoleOIDCEnv" -}}
{{- $o := .Values.console.oidc -}}
{{- $a := .Values.nebariapp.auth -}}
{{- $secret := printf "%s-oidc-client" (include "om.name" (dict "root" . "component" "console")) -}}
{{- if $o.issuerURL }}
- {name: OIDC_ISSUER_URL, value: {{ $o.issuerURL | quote }}}
- {name: OIDC_CLIENT_ID, value: {{ required "console.oidc.clientID is required with console.oidc.issuerURL" $o.clientID | quote }}}
{{- else if and .Values.nebariapp.enabled $a.enabled $a.enforceAtGateway $a.provisionClient $a.forwardAccessToken }}
- name: OIDC_ISSUER_URL
  valueFrom: {secretKeyRef: {name: {{ $secret }}, key: issuer-url}}
- name: OIDC_CLIENT_ID
  valueFrom: {secretKeyRef: {name: {{ $secret }}, key: client-id}}
{{- else if .Values.nebariapp.enabled }}
{{- fail "nebariapp.enabled exposes the console, which shows everyone's usage, so it needs sign-in: keep nebariapp.auth.enabled, enforceAtGateway, provisionClient and forwardAccessToken on, or set console.oidc.issuerURL and clientID" }}
{{- end }}
{{- with $o.discoveryURL }}
- {name: OIDC_DISCOVERY_URL, value: {{ . | quote }}}
{{- end }}
{{- if and (or $o.issuerURL .Values.nebariapp.enabled) $a.groups }}
- {name: OIDC_GROUPS, value: {{ join "," $a.groups | quote }}}
{{- end }}
{{- end -}}
