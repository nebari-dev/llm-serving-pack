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
The namespace every resource of the subchart goes in: namespace; else, as
part of the pack (inPack), the pack's namespace from the pack's own helper;
else the release namespace.
*/}}
{{- define "om.namespace" -}}
{{- if .Values.namespace -}}
{{ .Values.namespace }}
{{- else if .Values.inPack -}}
{{- if not (hasKey (.Values.global | default dict) "namespace") -}}
{{- fail "inPack is set by the LLM serving pack; installed on its own, leave it false" -}}
{{- end -}}
{{ include "nebari-llm-serving.operatorNamespace" . }}
{{- else -}}
{{ .Release.Namespace }}
{{- end -}}
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

{{/*
A duration for OpenMeter's config, quoted. OpenMeter parses a string with
time.ParseDuration but takes a bare number as nanoseconds, so a value with
no unit (maxCommitWait: 2) would silently mean 2ns. Fails the render unless
the value is a string such as "2s", "500ms" or "1h30m".
Takes (dict "key" "<values path>" "value" <value>).
*/}}
{{- define "om.duration" -}}
{{- if not (and (kindIs "string" .value) (regexMatch "^(([0-9]+\\.?[0-9]*|\\.[0-9]+)(ns|us|µs|ms|s|m|h))+$" .value)) -}}
{{- fail (printf "%s must be a duration with a unit, such as \"2s\" or \"500ms\"; got %v" .key .value) -}}
{{- end -}}
{{- .value | quote -}}
{{- end -}}

{{/*
A duration string already checked by om.duration, in seconds, for comparing
two durations.
*/}}
{{- define "om.durationSeconds" -}}
{{- $unitSeconds := dict "ns" 1e-9 "us" 1e-6 "µs" 1e-6 "ms" 0.001 "s" 1.0 "m" 60.0 "h" 3600.0 -}}
{{- $total := 0.0 -}}
{{- range regexFindAll "([0-9]+\\.?[0-9]*|\\.[0-9]+)(ns|us|µs|ms|s|m|h)" . -1 -}}
{{- $number := regexFind "^([0-9]+\\.?[0-9]*|\\.[0-9]+)" . -}}
{{- $unit := trimPrefix $number . -}}
{{- $total = addf $total (mulf (float64 $number) (get $unitSeconds $unit)) -}}
{{- end -}}
{{- $total -}}
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
client, or CONSOLE_DEV_MODE with console.devMode. Fails to render otherwise:
the console shows everyone's usage.
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
{{- else if .Values.console.devMode }}
- {name: CONSOLE_DEV_MODE, value: "true"}
{{- else }}
{{- fail "the usage console shows everyone's usage, so it needs sign-in: set nebariapp.enabled (with its gateway sign-in) or console.oidc.issuerURL and clientID; on a dev cluster, console.devMode; or set console.enabled=false to leave the console out" }}
{{- end }}
{{- with $o.discoveryURL }}
- {name: OIDC_DISCOVERY_URL, value: {{ . | quote }}}
{{- end }}
{{- if and (or $o.issuerURL .Values.nebariapp.enabled) $a.groups }}
- {name: OIDC_GROUPS, value: {{ join "," $a.groups | quote }}}
{{- end }}
{{- end -}}
