{{- define "sba.fullname" -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}

{{- define "sba.labels" -}}
app.kubernetes.io/name: sb-analysis
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "sba.selector" -}}
app.kubernetes.io/name: sb-analysis
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .name }}
{{- end -}}

{{- define "sba.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (include "sba.fullname" .) -}}
{{- end -}}

{{/* The image of a component: the media image for media workers. */}}
{{- define "sba.image" -}}
{{- $img := ternary .root.Values.mediaImage .root.Values.image (default false .media) -}}
{{- printf "%s:%s" $img.repository ($img.tag | default .root.Chart.AppVersion) -}}
{{- end -}}

{{- define "sba.pullPolicy" -}}
{{- $img := ternary .root.Values.mediaImage .root.Values.image (default false .media) -}}
{{- $img.pullPolicy -}}
{{- end -}}

{{/* Variables shared by every container: the config file, then each
credential read from its secret, key by key. */}}
{{- define "sba.env" -}}
- name: SB_ANALYSIS_CONFIG
  value: /etc/sb-analysis/config.yaml
{{- if .Values.platformCredentials.enabled }}
{{- range $name, $key := .Values.platformCredentials.env }}
- name: {{ $name }}
  valueFrom:
    secretKeyRef: {name: {{ $.Values.platformCredentials.secret }}, key: {{ $key }}}
{{- end }}
{{- end }}
{{- if .Values.aiProvider.enabled }}
- name: SB_ANALYSIS_ANALYSIS__PROVIDERS__{{ .Values.aiProvider.provider | upper }}__API_KEY
  valueFrom:
    secretKeyRef: {name: {{ .Values.aiProvider.secret }}, key: {{ .Values.aiProvider.key }}}
{{- end }}
{{- end -}}

{{- define "sba.envFrom" -}}
{{- if .Values.secretEnv.existingSecret }}
envFrom:
  - secretRef:
      name: {{ .Values.secretEnv.existingSecret }}
{{- end }}
{{- end -}}

{{- define "sba.volumeMounts" -}}
- name: config
  mountPath: /etc/sb-analysis
  readOnly: true
{{- if and .Values.platformCredentials.enabled .Values.platformCredentials.files }}
- name: certs
  mountPath: /etc/sb-analysis-certs
  readOnly: true
{{- end }}
- name: tmp
  mountPath: /tmp
{{- end -}}

{{/* A component's volumes: dict "root" and "c" (the component). */}}
{{- define "sba.volumes" -}}
{{- include "sba.volumesFor" (dict "root" .root "configMap" (printf "%s-config" (include "sba.fullname" .root)) "tmpSizeLimit" .c.tmpSizeLimit) -}}
{{- end -}}

{{- define "sba.hookVolumes" -}}
{{- include "sba.volumesFor" (dict "root" . "configMap" (printf "%s-hook-config" (include "sba.fullname" .))) -}}
{{- end -}}

{{- define "sba.volumesFor" -}}
{{- $pc := .root.Values.platformCredentials -}}
- name: config
  configMap:
    name: {{ .configMap }}
{{- if and $pc.enabled $pc.files }}
- name: certs
  secret:
    secretName: {{ $pc.secret }}
    defaultMode: 0440
    items:
      {{- range $key, $file := $pc.files }}
      - {key: {{ $key }}, path: {{ $file }}}
      {{- end }}
{{- end }}
- name: tmp
  {{- if .tmpSizeLimit }}
  emptyDir: {sizeLimit: {{ .tmpSizeLimit }}}
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- end -}}
