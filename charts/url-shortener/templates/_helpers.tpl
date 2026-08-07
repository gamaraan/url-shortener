{{/*
Common helpers for the url-shortener chart.
*/}}

{{- define "url-shortener.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "url-shortener.namespace" -}}
{{- .Values.namespace | default .Chart.Name -}}
{{- end -}}

{{- define "url-shortener.labels" -}}
app.kubernetes.io/name: {{ include "url-shortener.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: url-shortener
{{- end -}}

{{- define "backend.labels" -}}
{{ include "url-shortener.labels" . }}
app.kubernetes.io/component: backend
{{- end -}}

{{- define "frontend.labels" -}}
{{ include "url-shortener.labels" . }}
app.kubernetes.io/component: frontend
{{- end -}}

{{- define "backend.fullname" -}}
{{- printf "%s-backend" (include "url-shortener.name" .) -}}
{{- end -}}

{{- define "frontend.fullname" -}}
{{- printf "%s-frontend" (include "url-shortener.name" .) -}}
{{- end -}}

{{- define "postgres.secretName" -}}
{{- printf "%s-postgres" (include "url-shortener.name" .) -}}
{{- end -}}

{{- define "postgres.databaseURL" -}}
{{- if .Values.postgres.url -}}
{{- .Values.postgres.url -}}
{{- else -}}
{{- printf "postgres://%s:%s@%s:%s/%s?sslmode=disable" .Values.postgres.user .Values.postgres.password .Values.postgres.host .Values.postgres.port .Values.postgres.database -}}
{{- end -}}
{{- end -}}