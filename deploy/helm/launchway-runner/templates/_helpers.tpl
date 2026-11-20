{{/* Names. The same definitions as the launchway chart's, for this chart's own name. */}}
{{- define "launchway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "launchway.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "launchway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "launchway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "launchway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "launchway.labels" -}}
helm.sh/chart: {{ include "launchway.chart" . }}
{{ include "launchway.selectorLabels" . }}
app.kubernetes.io/version: {{ include "launchway.imageTag" . | trunc 63 | trimSuffix "-" | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: launchway
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "launchway.componentLabels" -}}
{{- $ := index . 0 -}}
{{ include "launchway.labels" $ }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end -}}

{{- define "launchway.componentSelectorLabels" -}}
{{- $ := index . 0 -}}
{{ include "launchway.selectorLabels" $ }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end -}}

{{- define "launchway.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion -}}
{{- end -}}

{{- define "launchway.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (include "launchway.imageTag" .) -}}
{{- end -}}
{{- end -}}

{{- define "launchway.runnerServiceAccount" -}}
{{- if .Values.runner.serviceAccount.create -}}
{{- .Values.runner.serviceAccount.name | default (include "launchway.fullname" .) -}}
{{- else -}}
{{- .Values.runner.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{- define "launchway.stepNamespace" -}}
{{- .Values.runner.kubernetes.namespace | default .Release.Namespace -}}
{{- end -}}

{{- define "launchway.deployRunnerName" -}}
{{- printf "%s-deploy" (include "launchway.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Default soft spread across nodes (the launchway chart's, for the runner component). */}}
{{- define "launchway.spread" -}}
{{- $ := index . 0 -}}
{{- $component := index . 1 -}}
{{- $values := $.Values.runner -}}
{{- if $values.topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml $values.topologySpreadConstraints | nindent 2 }}
{{- else if $values.spreadAcrossNodes }}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- include "launchway.componentSelectorLabels" (list $ $component) | nindent 8 }}
    matchLabelKeys:
      - pod-template-hash
{{- end }}
{{- if $values.affinity }}
affinity:
  {{- toYaml $values.affinity | nindent 2 }}
{{- else if $values.spreadAcrossNodes }}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              {{- include "launchway.componentSelectorLabels" (list $ $component) | nindent 14 }}
{{- end }}
{{- end -}}

{{/*
Environment for the proxy and the CA bundle, for every runner container. The runners' own
connections (to the server, Git, the Kubernetes API) honour HTTPS_PROXY, HTTP_PROXY and
NO_PROXY; SSL_CERT_FILE replaces the system's CA certificates, so the bundle must hold the public
CAs needed as well as a private one.
*/}}
{{- define "launchway.connectionEnv" -}}
{{- $env := list -}}
{{- with .Values.proxy -}}
{{- if .httpsProxy }}{{ $env = append $env (dict "name" "HTTPS_PROXY" "value" .httpsProxy) }}{{ end -}}
{{- if .httpProxy }}{{ $env = append $env (dict "name" "HTTP_PROXY" "value" .httpProxy) }}{{ end -}}
{{- if .noProxy }}{{ $env = append $env (dict "name" "NO_PROXY" "value" .noProxy) }}{{ end -}}
{{- end -}}
{{- if .Values.caBundle.configMap -}}
{{- $f := printf "/etc/launchway-ca/%s" .Values.caBundle.key -}}
{{- $env = append $env (dict "name" "SSL_CERT_FILE" "value" $f) -}}
{{- $env = append $env (dict "name" "GIT_SSL_CAINFO" "value" $f) -}}
{{- end -}}
{{- toYaml (dict "env" $env) -}}
{{- end -}}

{{- define "launchway.connectionVolumes" -}}
{{- $v := list -}}
{{- $m := list -}}
{{- if .Values.caBundle.configMap -}}
{{- $v = append $v (dict "name" "ca-bundle" "configMap" (dict "name" .Values.caBundle.configMap)) -}}
{{- $m = append $m (dict "name" "ca-bundle" "mountPath" "/etc/launchway-ca" "readOnly" true) -}}
{{- end -}}
{{- toYaml (dict "volumes" $v "mounts" $m) -}}
{{- end -}}

{{/* Checks that run on every render. */}}
{{- define "launchway.validate" -}}
{{- if not (regexMatch "^https?://" (.Values.server.url | default "")) -}}
{{- fail "\n\nserver.url is required: the URL of the Launchway server the runners connect to, such as https://launchway.example.com.\n" -}}
{{- end -}}
{{- if not (or .Values.runner.enabled .Values.deployRunner.enabled) -}}
{{- fail "\n\nNothing to run: enable runner.enabled (build runners) or deployRunner.enabled (a deploy runner), or both.\n" -}}
{{- end -}}
{{- /* JOIN HOOK. One-time join tokens (runner identities, design 5.3) are being built. When a
runner release takes them, join.existingSecret will reach each pod as LAUNCHWAY_JOIN (and the
runner's --join flag) in place of the shared token, and each pod will enrol as an ephemeral
runner. Until then the setting is refused, so that nobody believes it is in force. */ -}}
{{- if .Values.join.existingSecret -}}
{{- fail "\n\njoin.existingSecret: runners of this release do not take join tokens yet. Use token.existingSecret (a runner or runner pool token).\n" -}}
{{- end -}}
{{- if and .Values.runner.enabled (not .Values.token.existingSecret) -}}
{{- fail "\n\ntoken.existingSecret is required: a Secret in this namespace holding the runner token (or a runner pool's token) from the server's administrator.\n" -}}
{{- end -}}
{{- if and .Values.deployRunner.enabled (not .Values.deployRunner.token.existingSecret) -}}
{{- fail "\n\ndeployRunner.token.existingSecret is required: a Secret holding the token of the deploy runner pool (runner_pools in the server's configuration) that serves deployRunner.target.\n" -}}
{{- end -}}
{{- if .Values.deployRunner.enabled -}}
{{- $r := .Values.deployRunner -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9-]{0,62}$" ($r.target | default "")) -}}
{{- fail "deployRunner.target must name the target this cluster is, as the server's environments name it" -}}
{{- end -}}
{{- if not $r.applications }}{{ fail "deployRunner.applications: list which projects may deploy which applications" }}{{ end -}}
{{- if not $r.repos }}{{ fail "deployRunner.repos: list the GitOps repositories deploy steps may commit to" }}{{ end -}}
{{- range $ns, $names := ($r.rbac.controlRollouts | default dict) -}}
{{- if not (has $ns ($r.rolloutNamespaces | default list)) -}}
{{- fail (printf "deployRunner.rbac.controlRollouts names namespace %s, which is not in deployRunner.rolloutNamespaces" $ns) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
