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

{{/* The database URL the chart stores in its own Secret: only when database.url is given. The bundled
database's URL is never a stored value: the servers build it from the Secret that holds its password
(see launchway.databaseEnv). */}}
{{- define "launchway.databaseURL" -}}
{{- .Values.database.url -}}
{{- end -}}

{{/* Checks that run on every render. */}}
{{- define "launchway.validate" -}}
{{- include "launchway.validateKnownValues" . -}}
{{- include "launchway.validateImageVersion" . -}}
{{- if and (not .Values.database.url) (not .Values.database.existingSecret) (not .Values.postgresql.enabled) -}}
{{- fail "\n\nLaunchway needs a PostgreSQL database shared by all servers. Either leave postgresql.enabled=true (the chart runs one), or set database.existingSecret (a Secret holding a postgres:// URL) or database.url.\nWithout one, each server would keep its own SQLite file and they would not agree.\n" -}}
{{- end -}}
{{- if and .Values.database.url .Values.database.existingSecret -}}
{{- fail "\n\nSet only one of database.url and database.existingSecret.\n" -}}
{{- end -}}
{{- if and .Values.database.url (not (regexMatch "^postgres(ql)?://" .Values.database.url)) -}}
{{- fail "\n\ndatabase.url must be a postgres:// or postgresql:// URL: several servers cannot share a SQLite file.\n" -}}
{{- end -}}
{{- /* The address is required where the chart needs it: a new installation with built-in accounts
       (links in invitations), or an Ingress or route to name. An installation that already runs
       with its own sign-in (auth.mode oidc, or none behind a private gateway) and publishes
       nothing through this chart keeps working with the values it has. */ -}}
{{- if or (eq (include "launchway.authMode" .) "local") .Values.ingress.enabled .Values.httpRoute.enabled -}}
{{- $_ := include "launchway.externalHost" . -}}
{{- end -}}
{{- include "launchway.validateStorage" . -}}
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
{{- if and $ai.serverSecretStores (not $models) (not (.Values.config.notifications | default dict).channels) (not (.Values.config.mail | default dict).host) -}}
{{- fail "ai.serverSecretStores is set, but ai.models is empty (and neither config.notifications.channels nor config.mail read their secrets from it)" -}}
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
{{- /* Trace export is written only when it is on, so the file stays readable by the version before. */ -}}
{{- if .Values.tracing.enabled -}}
{{- $tr := dict "enabled" true "endpoint" .Values.tracing.endpoint -}}
{{- if .Values.tracing.headers.existingSecret }}{{ $_ := set $tr "headers_env" "LAUNCHWAY_TRACING_HEADERS" }}{{ end -}}
{{- if not (kindIs "invalid" .Values.tracing.sampleRatio) }}{{ $_ := set $tr "sample_ratio" (float64 .Values.tracing.sampleRatio) }}{{ end -}}
{{- with .Values.tracing.viewerURLTemplate }}{{ $_ := set $tr "viewer_url_template" . }}{{ end -}}
{{- $_ := set $config "observability" (dict "tracing" $tr) -}}
{{- end -}}
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
{{- /* With the bundled database the artifact cap follows its volume (half of it, or what is set and fits). */ -}}
{{- if and (include "launchway.bundledDB" . | trim) (has (toString $a.store) (list "" "database" "<nil>")) (not $d.maxTotalGiB) -}}
{{- $_ := set $db "max_total_gib" (int64 (include "launchway.derivedArtifactCapGiB" .)) -}}
{{- /* The largest single file must fit under the cap (the server refuses to start otherwise): on a small volume, 512 MiB unless the person set one. */ -}}
{{- if and (lt (int64 (include "launchway.derivedArtifactCapGiB" .)) 3) (not $d.maxBlobMiB) -}}
{{- $_ := set $db "max_blob_mib" (int64 512) -}}
{{- end -}}
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
{{- /* Queues and limits, automation accounts, pull-request trust and notifications (installation
       settings, imported once like the others): only what is set. */ -}}
{{- $q := .Values.config.queues | default dict -}}
{{- $queues := dict -}}
{{- range $key, $value := dict "max_wait" $q.maxWait -}}
{{- if $value }}{{ $_ := set $queues $key (toString $value) }}{{ end -}}
{{- end -}}
{{- range $key, $value := dict "runs" $q.runs "pipeline_runs" $q.pipelineRuns "pull_request_runs" $q.pullRequestRuns "push_runs" $q.pushRuns "schedule_runs" $q.scheduleRuns "manual_runs" $q.manualRuns "automation_runs" $q.automationRuns -}}
{{- if not (kindIs "invalid" $value) }}{{ $_ := set $queues $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if kindIs "bool" $q.supersedePullRequests }}{{ $_ := set $queues "supersede_pull_requests" $q.supersedePullRequests }}{{ end -}}
{{- if $queues }}{{ $_ := set $config "queues" $queues }}{{ end -}}
{{- $au := .Values.config.automation | default dict -}}
{{- if not (kindIs "invalid" $au.accounts) }}{{ $_ := set $config "automation" (dict "accounts" $au.accounts) }}{{ end -}}
{{- $pr := .Values.config.pullRequests | default dict -}}
{{- if $pr.untrusted -}}
{{- if not (has (toString $pr.untrusted) (list "restrict" "allow")) }}{{ fail "config.pullRequests.untrusted is restrict or allow" }}{{ end -}}
{{- $_ := set $config "pull_requests" (dict "untrusted" (toString $pr.untrusted)) -}}
{{- end -}}
{{- $nt := .Values.config.notifications | default dict -}}
{{- if or $nt.channels $nt.rules -}}
{{- range $nt.channels | default list -}}
{{- if or (not .url) (not .url.store) (not .url.key) -}}
{{- fail (printf "config.notifications.channels: %s: url is a reference to a secret store, such as {store: vault, key: ci/chat-hook, field: url}: the address of a chat webhook is a credential and is never written into the configuration" (toString .name)) -}}
{{- end -}}
{{- end -}}
{{- $_ := set $config "notifications" (dict "channels" ($nt.channels | default list) "rules" ($nt.rules | default list)) -}}
{{- end -}}
{{- /* E-mail to people (the mail installation setting): only what is set, with the chart's camelCase names. */ -}}
{{- $ml := .Values.config.mail | default dict -}}
{{- if $ml -}}
{{- $mail := dict -}}
{{- range $key, $value := dict "host" $ml.host "security" $ml.security "from" $ml.from "public_url" $ml.publicUrl -}}
{{- if $value }}{{ $_ := set $mail $key (toString $value) }}{{ end -}}
{{- end -}}
{{- range $key, $value := dict "port" $ml.port "rate_per_minute" $ml.ratePerMinute "rate_per_hour" $ml.ratePerHour "max_attempts" $ml.maxAttempts -}}
{{- if $value }}{{ $_ := set $mail $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if kindIs "bool" $ml.allowPlain }}{{ $_ := set $mail "allow_plain" $ml.allowPlain }}{{ end -}}
{{- if kindIs "bool" $ml.includePrivateDetails }}{{ $_ := set $mail "include_private_details" $ml.includePrivateDetails }}{{ end -}}
{{- range $key, $ref := dict "username" $ml.username "password" $ml.password -}}
{{- if $ref }}{{ $_ := set $mail $key $ref }}{{ end -}}
{{- end -}}
{{- if not $ml.host }}{{ fail "config.mail.host is the mail server; set it, or remove config.mail" }}{{ end -}}
{{- $_ := set $config "mail" $mail -}}
{{- end -}}
{{- /* Behind an Ingress or a Gateway route the servers trust the proxy's forwarded headers (and set
       the cookie's Secure flag from them), unless extraConfig.server.trust_proxy says otherwise. */ -}}
{{- if and (or .Values.ingress.enabled .Values.httpRoute.enabled) (not (hasKey (get $extra "server" | default dict) "trust_proxy")) -}}
{{- $_ := set $config "server" (dict "trust_proxy" true) -}}
{{- end -}}
{{- with .Values.server.trustedProxies }}{{ $srv := get $config "server" | default dict }}{{ $_ := set $srv "trusted_proxies" . }}{{ $_ := set $config "server" $srv }}{{ end -}}
{{- with .Values.server.clientAddressHeader }}{{ $srv := get $config "server" | default dict }}{{ $_ := set $srv "client_address_header" . }}{{ $_ := set $config "server" $srv }}{{ end -}}
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

{{/* The server URL runners connect to: runner.serverURL, else the chart's Service. */}}
{{- define "launchway.runnerServerURL" -}}
{{- .Values.runner.serverURL | default (printf "http://%s.%s.svc:%v" (include "launchway.fullname" .) .Release.Namespace .Values.service.port) -}}
{{- end -}}

{{/*
The runners' configuration file, for `launchway runner --config`: the same settings as runner:
in the server configuration, plus the server URL and the data directory. Rendered by the
template shared with the launchway-runner chart (deploy/helm/shared).
*/}}
{{- define "launchway.runnerConfig" -}}
{{- include "launchway.shared.runnerConfig" (dict "root" . "r" .Values.runner "server" (include "launchway.runnerServerURL" .) "stepNamespace" (include "launchway.stepNamespace" .)) -}}
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

{{/* The environment that tells a server where to serve metrics: the pod's own address, on the metrics port.
     It is the environment, not the configuration file, so that servers of the version before an upgrade can still read the file. */}}
{{- define "launchway.metricsEnv" -}}
{{- if .Values.metrics.enabled }}
- name: LAUNCHWAY_METRICS_LISTEN
  value: {{ printf "0.0.0.0:%d" (int .Values.metrics.port) | quote }}
{{- if .Values.metrics.nameLabels }}
- name: LAUNCHWAY_METRICS_NAME_LABELS
  value: "true"
{{- end }}
{{- else }}
- name: LAUNCHWAY_METRICS_ENABLED
  value: "false"
{{- end }}
{{- include "launchway.shared.metricsEnv" . }}
{{- end }}
