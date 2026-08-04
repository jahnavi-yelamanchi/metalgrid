{{- define "metalgrid.labels" -}}
app.kubernetes.io/part-of: metalgrid
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
