{{/* Names */}}
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

{{/* Labels */}}
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

{{/* Labels for one component: server, runner or postgresql. Call with (list $ "server"). */}}
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

{{/* Image */}}
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

{{/* Service accounts */}}
{{- define "launchway.serverServiceAccount" -}}
{{- if .Values.server.serviceAccount.create -}}
{{- .Values.server.serviceAccount.name | default (printf "%s-server" (include "launchway.fullname" .)) -}}
{{- else -}}
{{- .Values.server.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{- define "launchway.runnerServiceAccount" -}}
{{- if .Values.runner.serviceAccount.create -}}
{{- .Values.runner.serviceAccount.name | default (printf "%s-runner" (include "launchway.fullname" .)) -}}
{{- else -}}
{{- .Values.runner.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{/* The namespace step pods run in. */}}
{{- define "launchway.stepNamespace" -}}
{{- .Values.runner.kubernetes.namespace | default .Release.Namespace -}}
{{- end -}}

{{/* The port in config.listen. */}}
{{- define "launchway.port" -}}
{{- $port := regexFind "[0-9]+$" .Values.config.listen -}}
{{- if not $port -}}
{{- fail (printf "config.listen must end in a port, such as 0.0.0.0:8080 (got %q)" .Values.config.listen) -}}
{{- end -}}
{{- $port -}}
{{- end -}}

{{/* Secrets */}}
{{- define "launchway.databaseSecretName" -}}
{{- .Values.database.existingSecret | default (printf "%s-database" (include "launchway.fullname" .)) -}}
{{- end -}}

{{- define "launchway.databaseSecretKey" -}}
{{- if .Values.database.existingSecret -}}{{ .Values.database.existingSecretKey }}{{- else -}}url{{- end -}}
{{- end -}}

{{- define "launchway.runnerTokenSecretName" -}}
{{- .Values.runnerToken.existingSecret | default (printf "%s-runner-token" (include "launchway.fullname" .)) -}}
{{- end -}}

{{- define "launchway.runnerTokenSecretKey" -}}
{{- if .Values.runnerToken.existingSecret -}}{{ .Values.runnerToken.existingSecretKey }}{{- else -}}token{{- end -}}
{{- end -}}

{{- define "launchway.postgresqlName" -}}
{{- printf "%s-postgresql" (include "launchway.fullname" .) -}}
{{- end -}}

{{/*
The development database's password: the value given, else the one already stored in
its Secret, else a new random one. Kept stable across upgrades.
*/}}
{{- define "launchway.postgresqlPassword" -}}
{{- if .Values.postgresql.password -}}
{{- .Values.postgresql.password -}}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "launchway.postgresqlName" .) -}}
{{- if and $existing $existing.data (index $existing.data "password") -}}
{{- index $existing.data "password" | b64dec -}}
{{- else -}}
{{- /* Generated once per render and cached, so the Secret and the URL agree. */ -}}
{{- if not (hasKey .Values.postgresql "_generatedPassword") -}}
{{- $_ := set .Values.postgresql "_generatedPassword" (randAlphaNum 32) -}}
{{- end -}}
{{- .Values.postgresql._generatedPassword -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The database URL the chart stores in its own Secret, if it stores one. */}}
{{- define "launchway.databaseURL" -}}
{{- if .Values.database.url -}}
{{- .Values.database.url -}}
{{- else if .Values.postgresql.enabled -}}
{{- printf "postgres://%s:%s@%s.%s.svc:5432/%s?sslmode=disable" (.Values.postgresql.username | urlquery) (include "launchway.postgresqlPassword" . | urlquery) (include "launchway.postgresqlName" .) .Release.Namespace .Values.postgresql.database -}}
{{- end -}}
{{- end -}}

{{/* Checks that run on every render. */}}
{{- define "launchway.validate" -}}
{{- $sources := 0 -}}
{{- if .Values.database.url }}{{ $sources = add1 $sources }}{{ end -}}
{{- if .Values.database.existingSecret }}{{ $sources = add1 $sources }}{{ end -}}
{{- if .Values.postgresql.enabled }}{{ $sources = add1 $sources }}{{ end -}}
{{- if eq $sources 0 -}}
{{- fail "\n\nLaunchway needs a PostgreSQL database shared by all servers. Set one of:\n  database.existingSecret (and database.existingSecretKey): a Secret holding a postgres:// URL\n  database.url: a postgres:// URL\n  postgresql.enabled=true: a single development database, for trying the chart only\nWithout one, each server would keep its own SQLite file and they would not agree.\n" -}}
{{- end -}}
{{- if gt $sources 1 -}}
{{- fail "\n\nSet only one of database.url, database.existingSecret and postgresql.enabled.\n" -}}
{{- end -}}
{{- if and .Values.database.url (not (regexMatch "^postgres(ql)?://" .Values.database.url)) -}}
{{- fail "\n\ndatabase.url must be a postgres:// or postgresql:// URL: several servers cannot share a SQLite file.\n" -}}
{{- end -}}
{{- end -}}

{{/*
The server configuration file. extraConfig is merged at the top level; the keys the chart
owns may not appear in it.
*/}}
{{- define "launchway.serverConfig" -}}
{{- $extra := deepCopy (.Values.config.extraConfig | default dict) -}}
{{- range $key := list "listen" "data" "database" "projects" "tests" "loops" "schedules" -}}
{{- if hasKey $extra $key -}}
{{- if eq $key "database" -}}
{{- fail "config.extraConfig.database is not allowed: the database URL must not be in the ConfigMap. Use database.existingSecret or database.url." -}}
{{- else if eq $key "data" -}}
{{- fail "config.extraConfig.data is not allowed: the chart mounts the data directory at /var/lib/launchway." -}}
{{- else -}}
{{- fail (printf "config.extraConfig.%s is not allowed: use config.%s." $key $key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $runner := deepCopy (get $extra "runner" | default dict) -}}
{{- if and (hasKey $runner "enabled") $runner.enabled -}}
{{- fail "config.extraConfig.runner.enabled may not be true: servers run without a built-in runner; use the runner.* values." -}}
{{- end -}}
{{- $_ := set $runner "enabled" false -}}
{{- $_ := set $extra "runner" $runner -}}
{{- $config := dict "listen" .Values.config.listen "data" "/var/lib/launchway" "projects" (.Values.config.projects | default list) -}}
{{- $t := .Values.config.tests | default dict -}}
{{- $tests := dict -}}
{{- range $key, $value := dict "max_upload_mib" $t.maxUploadMiB "max_cases_per_run" $t.maxCasesPerRun "max_cases_per_project" $t.maxCasesPerProject -}}
{{- if $value }}{{ $_ := set $tests $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $tests }}{{ $_ := set $config "tests" $tests }}{{ end -}}
{{- with .Values.config.loops }}{{ if .maxMembers }}{{ $_ := set $config "loops" (dict "max_members" (int64 .maxMembers)) }}{{ end }}{{ end -}}
{{- with .Values.config.schedules }}{{ if not (kindIs "invalid" .enabled) }}{{ $_ := set $config "schedules" (dict "enabled" .enabled) }}{{ end }}{{ end -}}
{{- with .Values.config.tokens }}{{ if .maxDays }}{{ $_ := set $config "auth" (dict "tokens" (dict "max_days" (int64 .maxDays))) }}{{ end }}{{ end -}}
{{- toYaml (merge $config $extra) -}}
{{- end -}}

{{/*
The runners' configuration file, for `launchway runner --config`: the same settings as runner:
in the server configuration, plus the server URL and the data directory.
*/}}
{{- define "launchway.runnerConfig" -}}
{{- $fullname := include "launchway.fullname" . -}}
{{- $k := .Values.runner.kubernetes -}}
{{- $kube := dict "namespace" (include "launchway.stepNamespace" .) -}}
{{- range $key, $value := dict "helper_image" $k.helperImage "service_account" $k.serviceAccount "node_selector" $k.nodeSelector "cpu" $k.cpu "memory" $k.memory "memory_limit" $k.memoryLimit "image_pull_secrets" $k.imagePullSecrets "keep_failed" $k.keepFailed "start_timeout" $k.startTimeout -}}
{{- if $value }}{{ $_ := set $kube $key $value }}{{ end -}}
{{- end -}}
{{- if eq (toString $k.restricted) "false" }}
{{- $_ := set $kube "restricted" false -}}
{{- else }}
{{- range $key, $value := dict "run_as_user" $k.runAsUser "run_as_group" $k.runAsGroup -}}
{{- if not (kindIs "invalid" $value) }}{{ $_ := set $kube $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- $server := .Values.runner.serverURL | default (printf "http://%s.%s.svc:%v" $fullname .Release.Namespace .Values.service.port) -}}
{{- $r := .Values.runner.reports | default dict -}}
{{- $reports := dict -}}
{{- range $key, $value := dict "max_files" $r.maxFiles "max_file_mib" $r.maxFileMiB "max_total_mib" $r.maxTotalMiB -}}
{{- if $value }}{{ $_ := set $reports $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- $runner := dict "server" $server "data" "/var/lib/launchway-runner" "concurrency" .Values.runner.concurrency "executor" "kubernetes" "kubernetes" $kube "secrets" (.Values.runner.secretStores | default dict) -}}
{{- /* Only when set: a runner of the version before does not know the setting (see the note on defaults in values.yaml). */ -}}
{{- if $reports }}{{ $_ := set $runner "reports" $reports }}{{ end -}}
{{- toYaml $runner -}}
{{- end -}}

{{/* Default soft spread across nodes, for one component. Call with (list $ "server"). */}}
{{- define "launchway.spread" -}}
{{- $ := index . 0 -}}
{{- $component := index . 1 -}}
{{- $values := index $.Values $component -}}
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
