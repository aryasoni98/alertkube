{{/* vim: set filetype=mustache: */}}
{{- define "alertkube.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "alertkube.fullname" -}}
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

{{- define "alertkube.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "alertkube.labels" -}}
helm.sh/chart: {{ include "alertkube.chart" . }}
{{ include "alertkube.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "alertkube.selectorLabels" -}}
app.kubernetes.io/name: {{ include "alertkube.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "alertkube.slackWebhookSecret" -}}
{{- if .Values.slack.webhookUrlSecretKeyRef.name }}
  secretKeyRef:
    key: {{ .Values.slack.webhookUrlSecretKeyRef.key }}
    name: {{ .Values.slack.webhookUrlSecretKeyRef.name }}
{{- else }}
  secretKeyRef:
    key: slackWebhookUrl
    name: {{ include "alertkube.fullname" . }}
    optional: true
{{- end }}
{{- end }}

{{- define "alertkube.pagerdutyRoutingKey" -}}
{{- if .Values.pagerduty.routingKeySecretKeyRef.name }}
  secretKeyRef:
    key: {{ .Values.pagerduty.routingKeySecretKeyRef.key }}
    name: {{ .Values.pagerduty.routingKeySecretKeyRef.name }}
{{- else }}
  secretKeyRef:
    key: pagerdutyRoutingKey
    name: {{ include "alertkube.fullname" . }}
    optional: true
{{- end }}
{{- end }}

{{- define "alertkube.teamsWebhookUrl" -}}
{{- if .Values.teams.webhookUrlSecretKeyRef.name }}
  secretKeyRef:
    key: {{ .Values.teams.webhookUrlSecretKeyRef.key }}
    name: {{ .Values.teams.webhookUrlSecretKeyRef.name }}
{{- else }}
  secretKeyRef:
    key: teamsWebhookUrl
    name: {{ include "alertkube.fullname" . }}
    optional: true
{{- end }}
{{- end }}

{{- define "alertkube.genericWebhookUrl" -}}
{{- if .Values.genericWebhook.urlSecretKeyRef.name }}
  secretKeyRef:
    key: {{ .Values.genericWebhook.urlSecretKeyRef.key }}
    name: {{ .Values.genericWebhook.urlSecretKeyRef.name }}
{{- else }}
  secretKeyRef:
    key: genericWebhookUrl
    name: {{ include "alertkube.fullname" . }}
    optional: true
{{- end }}
{{- end }}
