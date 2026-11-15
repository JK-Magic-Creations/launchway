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
{{- toYaml $out -}}
{{- end -}}

{{/* The deploy runner's file, for `launchway runner --config`. */}}
{{- define "launchway.deployRunnerConfig" -}}
{{- $r := .Values.delivery.deployRunner -}}
{{- $target := dict "kubernetes" (dict "in_cluster" true) "argocd_namespace" $r.argocdNamespace "applications" $r.applications "rollout_namespaces" ($r.rolloutNamespaces | default list) -}}
{{- if not (kindIs "invalid" $r.refresh) }}{{ $_ := set $target "refresh" $r.refresh }}{{ end -}}
{{- $server := .Values.runner.serverURL | default (printf "http://%s.%s.svc:%v" (include "launchway.fullname" .) .Release.Namespace .Values.service.port) -}}
{{- toYaml (dict "server" $server "data" "/var/lib/launchway-runner" "secrets" ($r.secretStores | default dict) "deploy" (dict "targets" (dict $r.target $target) "repos" $r.repos)) -}}
{{- end -}}

{{/* Checks of the delivery values. */}}
{{- define "launchway.validateDelivery" -}}
{{- $r := .Values.delivery.deployRunner -}}
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
