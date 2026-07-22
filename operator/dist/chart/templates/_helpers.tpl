{{- define "chart.name" -}}
{{- if .Chart }}
  {{- if .Chart.Name }}
    {{- .Chart.Name | trunc 63 | trimSuffix "-" }}
  {{- else if .Values.nameOverride }}
    {{ .Values.nameOverride | trunc 63 | trimSuffix "-" }}
  {{- else }}
    operator
  {{- end }}
{{- else }}
  operator
{{- end }}
{{- end }}


{{/*
Fail the install when goblin has no way to do its job: no LLM key to reason
with, or no Telegram credentials to ask a human with. Called once from
NOTES.txt-independent templates so `helm install --dry-run` reports it too.
*/}}
{{- define "goblin.validateValues" -}}
{{- if and (not .Values.llm.apiKey) (not .Values.llm.existingSecret) -}}
{{- fail "goblin: an LLM API key is required. Set llm.apiKey=<key>, or llm.existingSecret=<name> for a Secret you manage yourself (it must contain the key LLM_API_KEY)." -}}
{{- end -}}
{{- if .Values.telegram.enabled -}}
{{- if and (not .Values.telegram.existingSecret) (or (not .Values.telegram.botToken) (not .Values.telegram.chatID)) -}}
{{- fail "goblin: Telegram credentials are required — the scout asks a human before it acts. Set telegram.botToken and telegram.chatID, or telegram.existingSecret=<name>. To run without a notifier, set telegram.enabled=false." -}}
{{- end -}}
{{- end -}}
{{- end }}


{{/*
Image tag: an explicit value wins, otherwise the appVersion this chart was
released with. That is what pins an install to a known-good build.
*/}}
A date tag such as 20260722 arrives from --set as a number, so every tag is
coerced to a string before it is compared or concatenated.
*/}}
{{- define "goblin.operatorTag" -}}
{{- .Values.controllerManager.container.image.tag | toString | default (.Chart.AppVersion | toString) -}}
{{- end }}


{{- define "goblin.scoutTag" -}}
{{- .Values.scout.image.tag | toString | default (.Chart.AppVersion | toString) -}}
{{- end }}


{{- define "goblin.operatorImage" -}}
{{- printf "%s:%s" .Values.controllerManager.container.image.repository (include "goblin.operatorTag" .) -}}
{{- end }}


{{- define "goblin.scoutImage" -}}
{{- printf "%s:%s" .Values.scout.image.repository (include "goblin.scoutTag" .) -}}
{{- end }}


{{/*
Only a date tag (20260722) is immutable — latest, dev and branch tags all move,
and a moved tag with IfNotPresent leaves a stale image running forever. So
pinned is the narrow case and Always is the safe default.
*/}}
{{- define "goblin.scoutPullPolicy" -}}
{{- if .Values.scout.image.pullPolicy -}}
{{- .Values.scout.image.pullPolicy -}}
{{- else if regexMatch "^[0-9]{8}$" (include "goblin.scoutTag" .) -}}
IfNotPresent
{{- else -}}
Always
{{- end -}}
{{- end }}


{{- define "goblin.llmSecretName" -}}
{{- .Values.llm.existingSecret | default "goblin-scout-secrets" -}}
{{- end }}


{{- define "goblin.hornSecretName" -}}
{{- .Values.telegram.existingSecret | default "goblin-horn-secrets" -}}
{{- end }}


{{- define "chart.labels" -}}
{{- if .Chart.AppVersion -}}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- if .Chart.Version }}
helm.sh/chart: {{ .Chart.Version | quote }}
{{- end }}
app.kubernetes.io/name: {{ include "chart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{- define "chart.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}


{{- define "chart.hasMutatingWebhooks" -}}
{{- $hasMutating := false }}
{{- range . }}
  {{- if eq .type "mutating" }}
    $hasMutating = true }}{{- end }}
{{- end }}
{{ $hasMutating }}}}{{- end }}


{{- define "chart.hasValidatingWebhooks" -}}
{{- $hasValidating := false }}
{{- range . }}
  {{- if eq .type "validating" }}
    $hasValidating = true }}{{- end }}
{{- end }}
{{ $hasValidating }}}}{{- end }}
