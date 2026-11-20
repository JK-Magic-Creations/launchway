{{/*
Delivery: deploy steps and the deploy runner. None of this is rendered unless
delivery.environments or delivery.deployRunner.enabled is set, so that with the default values
the configuration stays readable by releases that do not know these settings.
*/}}

{{- define "launchway.deployPoolName" -}}
{{- printf "deploy-%s" .Values.delivery.deployRunner.target -}}
{{- end -}}

{{- define "launchway.deployRunnerName" -}}
{{- printf "%s-deploy-runner" (include "launchway.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "launchway.deployPoolSecretName" -}}
{{- .Values.delivery.deployRunner.poolToken.existingSecret | default (printf "%s-deploy-pool-token" (include "launchway.fullname" .)) -}}
{{- end -}}

{{- define "launchway.deployPoolSecretKey" -}}
{{- if .Values.delivery.deployRunner.poolToken.existingSecret -}}{{ .Values.delivery.deployRunner.poolToken.existingSecretKey }}{{- else -}}token{{- end -}}
{{- end -}}

{{/* The settings the servers' configuration gains for delivery: empty unless set. */}}
{{- define "launchway.deliveryServerConfig" -}}
{{- $d := .Values.delivery | default dict -}}
{{- $out := dict -}}
{{- if $d.environments -}}
{{- $_ := set $out "environments" $d.environments -}}
{{- end -}}
{{- if and $d.deployRunner $d.deployRunner.enabled -}}
{{- $_ := set $out "runner_pools" (list (dict "name" (include "launchway.deployPoolName" .) "token_env" "LAUNCHWAY_DEPLOY_POOL_TOKEN" "targets" (list $d.deployRunner.target))) -}}
{{- end -}}
{{- /* Health sources (schema 19): the servers know only their names, types and projects. */ -}}
{{- $sources := list -}}
{{- range ($d.verification | default dict).sources | default list -}}
{{- $sources = append $sources (dict "name" .name "type" .type "projects" (.projects | default list)) -}}
{{- end -}}
{{- if $sources }}{{ $_ := set $out "verification" (dict "sources" $sources) }}{{ end -}}
{{- toYaml $out -}}
{{- end -}}

{{/* The deploy runner's file, for `launchway runner --config`. */}}
{{- define "launchway.deployRunnerConfig" -}}
{{- $r := .Values.delivery.deployRunner -}}
{{- $target := dict "kubernetes" (dict "in_cluster" true) "argocd_namespace" $r.argocdNamespace "applications" $r.applications "rollout_namespaces" ($r.rolloutNamespaces | default list) -}}
{{- if not (kindIs "invalid" $r.refresh) }}{{ $_ := set $target "refresh" $r.refresh }}{{ end -}}
{{- $server := .Values.runner.serverURL | default (printf "http://%s.%s.svc:%v" (include "launchway.fullname" .) .Release.Namespace .Values.service.port) -}}
{{- $file := dict "server" $server "data" "/var/lib/launchway-runner" "secrets" ($r.secretStores | default dict) "deploy" (dict "targets" (dict $r.target $target) "repos" $r.repos) -}}
{{- /* Only when set: a deploy runner of the version before does not know these settings. */ -}}
{{- with include "launchway.healthSources" . | fromYaml }}{{ if . }}{{ $_ := set $file "health_sources" . }}{{ end }}{{ end -}}
{{- if eq (toString $r.ai) "true" }}{{ $_ := set $file "ai" true }}{{ end -}}
{{- toYaml $file -}}
{{- end -}}

{{/* The deploy runner's health_sources: each of delivery.verification.sources by name, with
where it is and how to log in (credentials are secret references only). Empty when none is set. */}}
{{- define "launchway.healthSources" -}}
{{- $out := dict -}}
{{- range (.Values.delivery.verification | default dict).sources | default list -}}
{{- $s := dict "type" .type "url" .url "projects" (.projects | default list) -}}
{{- with .auth -}}
{{- $a := dict "secret" .secret -}}
{{- range $key, $value := dict "type" .type "user" .user "header" .header -}}
{{- if $value }}{{ $_ := set $a $key $value }}{{ end -}}
{{- end -}}
{{- $_ := set $s "auth" $a -}}
{{- end -}}
{{- range $key, $value := dict "headers" .headers "ca_file" .caFile "timeout" .timeout "statements" .statements "indices" .indices "time_field" .timeField "message_field" .messageField -}}
{{- if $value }}{{ $_ := set $s $key $value }}{{ end -}}
{{- end -}}
{{- with .limits -}}
{{- $l := dict -}}
{{- range $key, $value := dict "max_series" .maxSeries "max_points" .maxPoints "max_lines" .maxLines "max_bytes" .maxBytes -}}
{{- if $value }}{{ $_ := set $l $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- if $l }}{{ $_ := set $s "limits" $l }}{{ end -}}
{{- end -}}
{{- $_ := set $out .name $s -}}
{{- end -}}
{{- if $out }}{{ toYaml $out }}{{ end -}}
{{- end -}}

{{/* Checks of the delivery values. */}}
{{- define "launchway.validateDelivery" -}}
{{- $r := .Values.delivery.deployRunner -}}
{{- $sources := (.Values.delivery.verification | default dict).sources | default list -}}
{{- if and $sources (not $r.enabled) -}}
{{- fail "delivery.verification.sources: health sources are read by the deploy runner; enable delivery.deployRunner" -}}
{{- end -}}
{{- $seen := dict -}}
{{- range $sources -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9-]{0,62}$" (toString .name)) -}}
{{- fail (printf "delivery.verification.sources: name %q: use lower-case letters, digits and hyphens" (toString .name)) -}}
{{- end -}}
{{- if hasKey $seen .name }}{{ fail (printf "delivery.verification.sources: %s is listed twice" .name) }}{{ end -}}
{{- $_ := set $seen .name true -}}
{{- if not .projects -}}
{{- fail (printf "delivery.verification.sources: %s: list the projects that may use it under projects (patterns such as shop-* work)" .name) -}}
{{- end -}}
{{- if not .url }}{{ fail (printf "delivery.verification.sources: %s: url is missing" .name) }}{{ end -}}
{{- $name := .name -}}
{{- with .auth -}}
{{- if not (and .secret .secret.store .secret.key) -}}
{{- fail (printf "delivery.verification.sources: %s: auth.secret must name a store and a key, such as {store: vault, key: ci/prometheus, field: token}" $name) -}}
{{- end -}}
{{- if not (hasKey ($r.secretStores | default dict) (toString .secret.store)) -}}
{{- fail (printf "delivery.verification.sources: %s: auth.secret.store %q is not in delivery.deployRunner.secretStores" $name (toString .secret.store)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (eq (toString $r.ai) "true") (not (.Values.ai | default dict).models) -}}
{{- fail "delivery.deployRunner.ai is true, but ai.models is empty: list the models it may call" -}}
{{- end -}}
{{- if $r.enabled -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9-]{0,62}$" ($r.target | default "")) -}}
{{- fail "delivery.deployRunner.target must name the target this cluster is, as delivery.environments names it" -}}
{{- end -}}
{{- $known := false -}}
{{- range .Values.delivery.environments | default list }}{{ if eq .target $r.target }}{{ $known = true }}{{ end }}{{ end -}}
{{- if not $known -}}
{{- fail (printf "delivery.deployRunner.target is %q, but no environment in delivery.environments has that target" $r.target) -}}
{{- end -}}
{{- if not $r.applications -}}
{{- fail "delivery.deployRunner.applications: list which projects may deploy which applications" -}}
{{- end -}}
{{- if not $r.repos -}}
{{- fail "delivery.deployRunner.repos: list the GitOps repositories deploy steps may commit to" -}}
{{- end -}}
{{- range $ns, $names := ($r.rbac.controlRollouts | default dict) -}}
{{- if not (has $ns ($r.rolloutNamespaces | default list)) -}}
{{- fail (printf "delivery.deployRunner.rbac.controlRollouts names namespace %s, which is not in delivery.deployRunner.rolloutNamespaces" $ns) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The fields of a rollout's status the deploy runner may not change (Argo Rollouts v1.8
CRD): everything but pauseConditions (promote), promoteFull and abort (abort, retry). */}}
{{- define "launchway.rolloutStatusFixed" -}}
HPAReplicas abortedAt alb albs availableReplicas blueGreen canary collisionCount conditions controllerPause currentPodHash currentStepHash currentStepIndex message observedGeneration phase readyReplicas replicas restartedAt selector stableRS updatedReplicas workloadObservedGeneration
{{- end -}}
