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
{{- include "launchway.validateAI" . -}}
{{- end -}}

{{/* Checks of the model registry (ai.*) and of who may call models. */}}
{{- define "launchway.validateAI" -}}
{{- $ai := .Values.ai | default dict -}}
{{- $models := $ai.models | default list -}}
{{- $seen := dict -}}
{{- range $models -}}
{{- if not (regexMatch "^[a-z][a-z0-9-]{0,39}$" (toString .name)) -}}
{{- fail (printf "ai.models: name %q must start with a letter and use only a-z, 0-9 and -, at most 40 characters" (toString .name)) -}}
{{- end -}}
{{- if hasKey $seen .name }}{{ fail (printf "ai.models: %s is listed twice" .name) }}{{ end -}}
{{- $_ := set $seen .name true -}}
{{- if not (has (toString .provider) (list "anthropic" "openai")) -}}
{{- fail (printf "ai.models: %s: provider %q: use anthropic, or openai for any OpenAI-compatible server" .name (toString .provider)) -}}
{{- end -}}
{{- if not .model }}{{ fail (printf "ai.models: %s: model is missing: the provider's model id" .name) }}{{ end -}}
{{- if and (eq (toString .provider) "openai") (not .endpoint) -}}
{{- fail (printf "ai.models: %s: endpoint is required for provider openai, such as http://ollama.ai.svc:11434/v1" .name) -}}
{{- end -}}
{{- if and (eq (toString .provider) "anthropic") (not .endpoint) (not .key) -}}
{{- fail (printf "ai.models: %s: key is required for the hosted API: name the secret that holds the API key, such as {store: vault, key: ai/anthropic, field: key}" .name) -}}
{{- end -}}
{{- if and .key (not (and .key.store .key.key)) -}}
{{- fail (printf "ai.models: %s: key must name a store and a key, such as {store: vault, key: ai/anthropic, field: key}" .name) -}}
{{- end -}}
{{- end -}}
{{- if and $ai.serverSecretStores (not $models) -}}
{{- fail "ai.serverSecretStores is set, but ai.models is empty" -}}
{{- end -}}
{{- if and (eq (toString .Values.runner.ai) "true") (not $models) -}}
{{- fail "runner.ai is true, but ai.models is empty: list the models runners may call" -}}
{{- end -}}
{{- end -}}

{{/*
The server configuration file. extraConfig is merged at the top level; the keys the chart
owns may not appear in it.
*/}}
{{- define "launchway.serverConfig" -}}
{{- $extra := deepCopy (.Values.config.extraConfig | default dict) -}}
{{- range $key := list "listen" "data" "database" "projects" "tests" "artifacts" "findings" "reports" "loops" "schedules" "policy" "mcp" "verification" "ai" -}}
{{- if hasKey $extra $key -}}
{{- if eq $key "verification" -}}
{{- fail "config.extraConfig.verification is not allowed: use delivery.verification.sources, which also gives the deploy runner the sources." -}}
{{- else if eq $key "ai" -}}
{{- fail "config.extraConfig.ai is not allowed: use ai.models and ai.serverSecretStores." -}}
{{- else if eq $key "database" -}}
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
{{- $config := dict "listen" .Values.config.listen "data" "/var/lib/launchway" -}}
{{- /* Since schema 23 the file is bootstrap only: projects (and every other setting an
       administrator manages) are imported into the database once, by the first server that
       starts, and the file's copies are ignored after that. The chart writes them only when set. */ -}}
{{- with .Values.config.projects }}{{ $_ := set $config "projects" . }}{{ end -}}
{{- $t := .Values.config.tests | default dict -}}
{{- $tests := dict -}}
{{- range $key, $value := dict "max_upload_mib" $t.maxUploadMiB "max_cases_per_run" $t.maxCasesPerRun "max_cases_per_project" $t.maxCasesPerProject -}}
{{- if $value }}{{ $_ := set $tests $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $tests }}{{ $_ := set $config "tests" $tests }}{{ end -}}
{{- /* Artifacts and findings (schema 12): only what is set, so the version before can still read the file. */ -}}
{{- $a := .Values.config.artifacts | default dict -}}
{{- $arts := dict -}}
{{- range $key, $value := dict "store" $a.store "retain" $a.retain "max_retain" $a.maxRetain -}}
{{- if $value }}{{ $_ := set $arts $key (toString $value) }}{{ end -}}
{{- end -}}
{{- range $key, $value := dict "max_file_mib" $a.maxFileMiB "max_run_mib" $a.maxRunMiB "max_project_gib" $a.maxProjectGiB -}}
{{- if $value }}{{ $_ := set $arts $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- $s := $a.s3 | default dict -}}
{{- $s3 := dict -}}
{{- range $key, $value := dict "endpoint" $s.endpoint "region" $s.region "bucket" $s.bucket "prefix" $s.prefix -}}
{{- if $value }}{{ $_ := set $s3 $key (toString $value) }}{{ end -}}
{{- end -}}
{{- if kindIs "bool" $s.pathStyle }}{{ $_ := set $s3 "path_style" $s.pathStyle }}{{ end -}}
{{- if $s3 -}}
{{- if ne (toString $a.store) "s3" }}{{ fail "config.artifacts.s3 is set, but config.artifacts.store is not s3" }}{{ end -}}
{{- if not $s.existingSecret }}{{ fail "config.artifacts.s3 needs existingSecret: a Secret with AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY" }}{{ end -}}
{{- $_ := set $arts "s3" $s3 -}}
{{- else if eq (toString $a.store) "s3" -}}
{{- fail "config.artifacts.store is s3: set config.artifacts.s3.endpoint, bucket and existingSecret" -}}
{{- end -}}
{{- /* The database store's limits (schema 17): only what is set, so the version before can still read the file. */ -}}
{{- $d := $a.database | default dict -}}
{{- $db := dict -}}
{{- range $key, $value := dict "max_total_gib" $d.maxTotalGiB "max_blob_mib" $d.maxBlobMiB -}}
{{- if $value }}{{ $_ := set $db $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $db -}}
{{- if not (has (toString $a.store) (list "" "database" "<nil>")) }}{{ fail "config.artifacts.database is set, but config.artifacts.store is not database (or unset)" }}{{ end -}}
{{- $_ := set $arts "database" $db -}}
{{- end -}}
{{- if $arts }}{{ $_ := set $config "artifacts" $arts }}{{ end -}}
{{- $f := .Values.config.findings | default dict -}}
{{- $finds := dict -}}
{{- range $key, $value := dict "max_upload_mib" $f.maxUploadMiB "max_per_run" $f.maxPerRun "max_per_project" $f.maxPerProject "max_components_per_run" $f.maxComponentsPerRun "max_components_per_project" $f.maxComponentsPerProject "max_sbom_mib" $f.maxSbomMiB -}}
{{- if $value }}{{ $_ := set $finds $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $finds }}{{ $_ := set $config "findings" $finds }}{{ end -}}
{{- /* Test attachments, coverage and HTML reports (schema 18): only what is set. */ -}}
{{- $r := .Values.config.reports | default dict -}}
{{- $reps := dict -}}
{{- range $key, $value := dict "max_attachment_files" $r.maxAttachmentFiles "max_attachment_mib" $r.maxAttachmentMiB "max_html_files" $r.maxHtmlFiles "max_html_mib" $r.maxHtmlMiB "max_coverage_upload_mib" $r.maxCoverageUploadMiB "max_coverage_files_per_run" $r.maxCoverageFilesPerRun "max_coverage_files_per_project" $r.maxCoverageFilesPerProject -}}
{{- if $value }}{{ $_ := set $reps $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $r.origin }}{{ $_ := set $reps "origin" (toString $r.origin) }}{{ end -}}
{{- if $reps }}{{ $_ := set $config "reports" $reps }}{{ end -}}
{{- with .Values.config.loops }}{{ if .maxMembers }}{{ $_ := set $config "loops" (dict "max_members" (int64 .maxMembers)) }}{{ end }}{{ end -}}
{{- with .Values.config.schedules }}{{ if not (kindIs "invalid" .enabled) }}{{ $_ := set $config "schedules" (dict "enabled" .enabled) }}{{ end }}{{ end -}}
{{- with .Values.config.tokens }}{{ if .maxDays }}{{ $_ := set $config "auth" (dict "tokens" (dict "max_days" (int64 .maxDays))) }}{{ end }}{{ end -}}
{{- with include "launchway.policyConfig" . | fromYaml }}{{ if . }}{{ $_ := set $config "policy" . }}{{ end }}{{ end -}}
{{- /* The MCP endpoint: only what is set, so the version before can still read the file. */ -}}
{{- $m := .Values.config.mcp | default dict -}}
{{- $mcp := dict -}}
{{- if kindIs "bool" $m.enabled }}{{ $_ := set $mcp "enabled" $m.enabled }}{{ end -}}
{{- if $m.allowedOrigins }}{{ $_ := set $mcp "allowed_origins" $m.allowedOrigins }}{{ end -}}
{{- range $key, $value := dict "requests_per_minute" $m.requestsPerMinute "concurrent" $m.concurrent "total" $m.total -}}
{{- if $value }}{{ $_ := set $mcp $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- $o := $m.oauth | default dict -}}
{{- if $o.resource -}}
{{- $oauth := dict "resource" (toString $o.resource) -}}
{{- if $o.scopes }}{{ $_ := set $oauth "scopes" $o.scopes }}{{ end -}}
{{- $_ := set $mcp "oauth" $oauth -}}
{{- else if $o.scopes -}}
{{- fail "config.mcp.oauth.scopes is set, but config.mcp.oauth.resource is not" -}}
{{- end -}}
{{- if $mcp }}{{ $_ := set $config "mcp" $mcp }}{{ end -}}
{{- $config = merge $config (include "launchway.deliveryServerConfig" . | fromYaml) -}}
{{- /* The model registry (schema 19): only when set. */ -}}
{{- $ai := .Values.ai | default dict -}}
{{- $aiOut := dict -}}
{{- with $ai.models }}{{ $_ := set $aiOut "models" . }}{{ end -}}
{{- with $ai.serverSecretStores }}{{ $_ := set $aiOut "secrets" . }}{{ end -}}
{{- if $aiOut }}{{ $_ := set $config "ai" $aiOut }}{{ end -}}
{{- /* Runner pools and who manages runners (schemas 20 and 21): only when set. */ -}}
{{- if .Values.config.runnerPools -}}
{{- $_ := set $config "runner_pools" (concat (get $config "runner_pools" | default list) .Values.config.runnerPools) -}}
{{- end -}}
{{- if .Values.config.runnerAdmin }}{{ $_ := set $config "runner_admin" (toString .Values.config.runnerAdmin) }}{{ end -}}
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
{{- with include "launchway.runnerImages" . | fromYaml }}{{ if . }}{{ $_ := set $runner "images" . }}{{ end }}{{ end -}}
{{- if eq (toString .Values.runner.ai) "true" }}{{ $_ := set $runner "ai" true }}{{ end -}}
{{- /* Labels and the runner's own limits (schemas 20 and 21): only when set. */ -}}
{{- with .Values.runner.labels }}{{ $_ := set $runner "labels" . }}{{ end -}}
{{- with .Values.runner.accept }}{{ $_ := set $runner "accept" . }}{{ end -}}
{{- toYaml $runner -}}
{{- end -}}

{{/* The runners' images section (image steps), or nothing when no builder is set: a runner of
the version before does not know the setting. */}}
{{- define "launchway.runnerImages" -}}
{{- $i := .Values.runner.images | default dict -}}
{{- if $i.builders -}}
{{- $tls := "/var/run/launchway/buildkit" -}}
{{- $builders := list -}}
{{- range $i.builders -}}
{{- $b := dict "name" .name "address" .address -}}
{{- if hasPrefix "tcp://" .address -}}
{{- if not $i.tlsSecret }}{{ fail (printf "runner.images.builders: %s is reached over TCP, which needs mutual TLS: set runner.images.tlsSecret" .name) }}{{ end -}}
{{- $_ := set $b "ca_file" (printf "%s/ca.crt" $tls) -}}
{{- $_ := set $b "cert_file" (printf "%s/tls.crt" $tls) -}}
{{- $_ := set $b "key_file" (printf "%s/tls.key" $tls) -}}
{{- end -}}
{{- with .serverName }}{{ $_ := set $b "server_name" . }}{{ end -}}
{{- with .platforms }}{{ $_ := set $b "platforms" . }}{{ end -}}
{{- with .projects }}{{ $_ := set $b "projects" . }}{{ end -}}
{{- $builders = append $builders $b -}}
{{- end -}}
{{- $out := dict "builders" $builders -}}
{{- $regs := dict -}}
{{- range $name, $r := $i.registries -}}
{{- $reg := dict "host" $r.host -}}
{{- with $r.credentials }}{{ $_ := set $reg "credentials" (include "launchway.imageCredentials" . | fromYaml) }}{{ end -}}
{{- with $r.pullCredentials }}{{ $_ := set $reg "pull_credentials" (include "launchway.imageCredentials" . | fromYaml) }}{{ end -}}
{{- with $r.pullProjects }}{{ $_ := set $reg "pull_projects" . }}{{ end -}}
{{- with $r.caFile }}{{ $_ := set $reg "ca_file" . }}{{ end -}}
{{- if $r.insecure }}{{ $_ := set $reg "insecure" true }}{{ end -}}
{{- with $r.push }}{{ $_ := set $reg "push" . }}{{ end -}}
{{- $_ := set $regs $name $reg -}}
{{- end -}}
{{- if $regs }}{{ $_ := set $out "registries" $regs }}{{ end -}}
{{- if $i.signingSecret -}}
{{- $_ := set $out "signing" (dict "key_file" "/var/run/launchway/signing/cosign.key" "password_file" "/var/run/launchway/signing/cosign.password") -}}
{{- end -}}
{{- $sc := $i.scanner | default dict -}}
{{- $scanner := dict -}}
{{- with $sc.image }}{{ $_ := set $scanner "image" . }}{{ end -}}
{{- with $sc.dbRepository }}{{ $_ := set $scanner "db_repository" . }}{{ end -}}
{{- with $sc.javaDBRepository }}{{ $_ := set $scanner "java_db_repository" . }}{{ end -}}
{{- if $scanner }}{{ $_ := set $out "scanner" $scanner }}{{ end -}}
{{- with $i.maxContextMiB }}{{ $_ := set $out "max_context_mib" (int64 .) }}{{ end -}}
{{- with $i.maxImageMiB }}{{ $_ := set $out "max_image_mib" (int64 .) }}{{ end -}}
{{- toYaml $out -}}
{{- end -}}
{{- end -}}

{{- define "launchway.imageCredentials" -}}
{{- $c := dict "password" .password -}}
{{- with .username }}{{ $_ := set $c "username" . }}{{ end -}}
{{- with .usernameFrom }}{{ $_ := set $c "username_from" . }}{{ end -}}
{{- toYaml $c -}}
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

{{/* Delivery policies (schema 16), or nothing when none is set: a server of the version before
does not know the setting. */}}
{{- define "launchway.policyConfig" -}}
{{- $p := .Values.config.policy | default dict -}}
{{- $out := dict -}}
{{- with $p.waivers -}}
{{- $w := dict -}}
{{- if .allow }}{{ $_ := set $w "allow" (toString .allow) }}{{ end -}}
{{- if .maxDays }}{{ $_ := set $w "max_days" (int64 .maxDays) }}{{ end -}}
{{- if $w }}{{ $_ := set $out "waivers" $w }}{{ end -}}
{{- end -}}
{{- if $p.sources -}}
{{- $sources := list -}}
{{- range $p.sources -}}
{{- $s := dict "name" .name "library" .library "version" (toString .version) -}}
{{- if .path }}{{ $_ := set $s "path" .path }}{{ end -}}
{{- if .projects }}{{ $_ := set $s "projects" .projects }}{{ end -}}
{{- if .refresh }}{{ $_ := set $s "refresh" (toString .refresh) }}{{ end -}}
{{- if .staleAfter }}{{ $_ := set $s "stale_after" (toString .staleAfter) }}{{ end -}}
{{- $sources = append $sources $s -}}
{{- end -}}
{{- $_ := set $out "sources" $sources -}}
{{- end -}}
{{- if $p.policies }}{{ $_ := set $out "policies" $p.policies }}{{ end -}}
{{- if $out }}{{ toYaml $out }}{{ end -}}
{{- end -}}
