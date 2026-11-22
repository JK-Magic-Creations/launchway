{{/*
Runner templates shared by the launchway chart (servers and runners) and the launchway-runner
chart (runners only). The source is deploy/helm/shared/_runners.tpl; `make charts-sync` copies it
into each chart's templates/ as _shared-runners.tpl. Edit the source, never a copy:
internal/config/chart_test.go fails when a copy differs from it.

Every template takes a dict:
  root           the chart's top-level context ($)
  r              the runner's values (runner.* in both charts; delivery.deployRunner.* or
                 deployRunner.* for the deploy runner)
  name           the name of the runner's objects (Deployment, ConfigMap, Role, ...)
  sa             the ServiceAccount's name
  component      the app.kubernetes.io/component label
  server         the server URL the runner connects to
  stepNamespace  build runners: the namespace step pods run in
  tokenSecret, tokenKey   the Secret and key holding the runner (or pool) token
  env, volumes, mounts    extra env entries, volumes and volume mounts (proxy, CA bundle)
The including chart must define launchway.componentLabels, launchway.componentSelectorLabels,
launchway.image and launchway.spread, and have image, imagePullSecrets, podSecurityContext and
securityContext values.
*/}}

{{/* A build runner's file, for `launchway runner --config`: the same settings as runner: in the
server configuration, plus the server URL and the data directory. */}}
{{- define "launchway.shared.runnerConfig" -}}
{{- $k := .r.kubernetes -}}
{{- $kube := dict "namespace" .stepNamespace -}}
{{- range $key, $value := dict "helper_image" $k.helperImage "service_account" $k.serviceAccount "node_selector" $k.nodeSelector "priority_class_name" $k.priorityClassName "cpu" $k.cpu "memory" $k.memory "memory_limit" $k.memoryLimit "max_cpu" $k.maxCpu "max_memory" $k.maxMemory "image_pull_secrets" $k.imagePullSecrets "keep_failed" $k.keepFailed "max_pod_age" $k.maxPodAge "start_timeout" $k.startTimeout "pod" $k.pod "max_workspace" $k.maxWorkspace "max_workspaces" $k.maxWorkspaces -}}
{{- if $value }}{{ $_ := set $kube $key $value }}{{ end -}}
{{- end -}}
{{- if eq (toString $k.restricted) "false" }}
{{- $_ := set $kube "restricted" false -}}
{{- else }}
{{- range $key, $value := dict "run_as_user" $k.runAsUser "run_as_group" $k.runAsGroup -}}
{{- if not (kindIs "invalid" $value) }}{{ $_ := set $kube $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- $r := .r.reports | default dict -}}
{{- $reports := dict -}}
{{- range $key, $value := dict "max_files" $r.maxFiles "max_file_mib" $r.maxFileMiB "max_total_mib" $r.maxTotalMiB -}}
{{- if $value }}{{ $_ := set $reports $key (int64 $value) }}{{ end -}}
{{- end -}}
{{- $runner := dict "server" .server "data" "/var/lib/launchway-runner" "concurrency" .r.concurrency "executor" "kubernetes" "kubernetes" $kube "secrets" (.r.secretStores | default dict) -}}
{{- /* Only when set: a runner of the version before does not know the setting (see the note on defaults in values.yaml). */ -}}
{{- if $reports }}{{ $_ := set $runner "reports" $reports }}{{ end -}}
{{- with include "launchway.shared.runnerImages" .r | fromYaml }}{{ if . }}{{ $_ := set $runner "images" . }}{{ end }}{{ end -}}
{{- if eq (toString .r.ai) "true" }}{{ $_ := set $runner "ai" true }}{{ end -}}
{{- /* Labels and the runner's own limits (schemas 20 and 21): only when set. */ -}}
{{- with .r.labels }}{{ $_ := set $runner "labels" . }}{{ end -}}
{{- with .r.accept }}{{ $_ := set $runner "accept" . }}{{ end -}}
{{- toYaml $runner -}}
{{- end -}}

{{/* The runners' images section (image steps), or nothing when no builder is set: a runner of
the version before does not know the setting. Takes the runner's values. */}}
{{- define "launchway.shared.runnerImages" -}}
{{- $i := .images | default dict -}}
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
{{- with $r.credentials }}{{ $_ := set $reg "credentials" (include "launchway.shared.imageCredentials" . | fromYaml) }}{{ end -}}
{{- with $r.pullCredentials }}{{ $_ := set $reg "pull_credentials" (include "launchway.shared.imageCredentials" . | fromYaml) }}{{ end -}}
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

{{- define "launchway.shared.imageCredentials" -}}
{{- $c := dict "password" .password -}}
{{- with .username }}{{ $_ := set $c "username" . }}{{ end -}}
{{- with .usernameFrom }}{{ $_ := set $c "username_from" . }}{{ end -}}
{{- toYaml $c -}}
{{- end -}}

{{/*
What the Kubernetes executor needs: the step namespace (if the chart creates it), the runners'
ServiceAccount, and a Role and RoleBinding in the step namespace granting exactly what the
executor uses. Least privilege: pods, their exec and log, in that one namespace.
*/}}
{{- define "launchway.shared.executorAccess" -}}
{{- $root := .root -}}
{{- $k := .r.kubernetes -}}
{{- if and $k.createNamespace (eq (toString $k.restricted) "false") (eq (index ($k.namespaceLabels | default dict) "pod-security.kubernetes.io/enforce") "restricted") }}
{{- fail "runner.kubernetes.restricted is false, so step pods run as their images' users and do not meet Pod Security \"restricted\", which runner.kubernetes.namespaceLabels enforces on the step namespace. Set namespaceLabels.\"pod-security.kubernetes.io/enforce\" to baseline." }}
{{- end }}
{{- if and $k.createNamespace (ne .stepNamespace $root.Release.Namespace) }}
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .stepNamespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
    {{- with $k.namespaceLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
---
{{- end }}
{{- if .r.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .sa }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
  {{- with .r.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
# The token is mounted only into the runner pods, which ask for it.
automountServiceAccountToken: false
---
{{- end }}
{{- if .r.rbac.create }}
# Exactly what the Kubernetes executor uses, in the step namespace only.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .name }}
  namespace: {{ .stepNamespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "delete", "patch"]
  # Exec is a POST (SPDY) or a GET (WebSocket); which verb is checked depends on the
  # Kubernetes version, so both are granted.
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create", "get"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .name }}
  namespace: {{ .stepNamespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .name }}
subjects:
  - kind: ServiceAccount
    name: {{ .sa }}
    namespace: {{ $root.Release.Namespace }}
---
{{- end }}
{{- end -}}

{{/* The build runners' Deployment and PodDisruptionBudget. The ConfigMap .name holds runner.yaml. */}}
{{- define "launchway.shared.runnerDeployment" -}}
{{- $root := .root -}}
{{- $r := .r -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
spec:
  {{- if not (($r.autoscaling | default dict).enabled) }}
  replicas: {{ $r.replicas }}
  {{- end }}
  revisionHistoryLimit: 5
  selector:
    matchLabels:
      {{- include "launchway.componentSelectorLabels" (list $root .component) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "launchway.componentLabels" (list $root .component) | nindent 8 }}
        {{- with $r.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        checksum/config: {{ .config | sha256sum }}
        {{- include "launchway.shared.scrapeAnnotations" $root | nindent 8 }}
        {{- with $r.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ .sa }}
      # The Kubernetes executor finds the API server and its credentials through the
      # in-cluster configuration: this token and KUBERNETES_SERVICE_HOST/PORT.
      automountServiceAccountToken: true
      enableServiceLinks: false
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- range . }}
        - name: {{ . }}
        {{- end }}
      {{- end }}
      {{- with $r.priorityClassName }}
      priorityClassName: {{ . }}
      {{- end }}
      terminationGracePeriodSeconds: {{ $r.terminationGracePeriodSeconds }}
      securityContext:
        {{- toYaml $root.Values.podSecurityContext | nindent 8 }}
      {{- with include "launchway.spread" (list $root .component) | trim }}
      {{- . | nindent 6 }}
      {{- end }}
      {{- with $r.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $r.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: runner
          image: {{ include "launchway.image" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          args:
            - runner
            - --config=/etc/launchway-runner/runner.yaml
            - --health-listen=:{{ $r.healthPort }}
            {{- if (($root.Values.metrics | default dict).enabled) }}
            - --metrics-listen=:{{ $root.Values.metrics.port }}
            {{- end }}
            {{- range $r.extraArgs }}
            - {{ . | quote }}
            {{- end }}
          # The runner's name (and its identity on the server) defaults to the pod name.
          env:
            {{- include "launchway.shared.metricsEnv" $root | nindent 12 }}
            {{- if and $r.join $r.join.existingSecret }}
            # Each pod enrols with the join token at start (schema 20) and gets an identity of
            # its own, kept in its data volume.
            - name: LAUNCHWAY_JOIN
              valueFrom:
                secretKeyRef:
                  name: {{ $r.join.existingSecret }}
                  key: {{ $r.join.existingSecretKey | default "token" }}
            {{- else }}
            - name: LAUNCHWAY_RUNNER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .tokenSecret }}
                  key: {{ .tokenKey }}
            {{- end }}
            {{- with .env }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- with $r.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          ports:
            - name: health
              containerPort: {{ $r.healthPort }}
              protocol: TCP
            {{- if (($root.Values.metrics | default dict).enabled) }}
            - name: metrics
              containerPort: {{ $root.Values.metrics.port }}
              protocol: TCP
            {{- end }}
          {{- with $r.livenessProbe }}
          livenessProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with $r.readinessProbe }}
          readinessProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          resources:
            {{- toYaml $r.resources | nindent 12 }}
          securityContext:
            {{- toYaml $root.Values.securityContext | nindent 12 }}
          volumeMounts:
            - name: config
              mountPath: /etc/launchway-runner
              readOnly: true
            - name: data
              mountPath: /var/lib/launchway-runner
            - name: tmp
              mountPath: /tmp
            {{- with ($r.images | default dict) }}
            {{- if and .builders .tlsSecret }}
            - name: buildkit-tls
              mountPath: /var/run/launchway/buildkit
              readOnly: true
            {{- end }}
            {{- if and .builders .signingSecret }}
            - name: signing
              mountPath: /var/run/launchway/signing
              readOnly: true
            {{- end }}
            {{- end }}
            {{- with .mounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- with $r.extraVolumeMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ .name }}
        - name: data
          emptyDir:
            {{- with $r.dataVolume.sizeLimit }}
            sizeLimit: {{ . }}
            {{- end }}
        - name: tmp
          emptyDir:
            sizeLimit: 256Mi
        {{- with ($r.images | default dict) }}
        {{- if and .builders .tlsSecret }}
        - name: buildkit-tls
          secret:
            secretName: {{ .tlsSecret }}
            defaultMode: 0440
        {{- end }}
        {{- if and .builders .signingSecret }}
        - name: signing
          secret:
            secretName: {{ .signingSecret }}
            defaultMode: 0440
        {{- end }}
        {{- end }}
        {{- with .volumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- with $r.extraVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
{{- if $r.podDisruptionBudget.enabled }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
spec:
  maxUnavailable: {{ $r.podDisruptionBudget.maxUnavailable }}
  unhealthyPodEvictionPolicy: AlwaysAllow
  selector:
    matchLabels:
      {{- include "launchway.componentSelectorLabels" (list $root .component) | nindent 6 }}
{{- end }}
{{- end -}}

{{/* The deploy runner's file, for `launchway runner --config`. */}}
{{- define "launchway.shared.deployRunnerConfig" -}}
{{- $r := .r -}}
{{- $target := dict "kubernetes" (dict "in_cluster" true) "argocd_namespace" $r.argocdNamespace "applications" $r.applications "rollout_namespaces" ($r.rolloutNamespaces | default list) -}}
{{- if not (kindIs "invalid" $r.refresh) }}{{ $_ := set $target "refresh" $r.refresh }}{{ end -}}
{{- /* Only what rbac.inspect grants: the runner's own settings say the same, so the page offers only what works. */ -}}
{{- $in := $r.rbac.inspect | default dict -}}
{{- if $in.read }}
{{- $ins := dict "read" ($in.read | default false) "logs" (and $in.read $in.logs | default false) "control" (and $in.read (not (empty $in.controlWorkloads)) | default false) -}}
{{- $_ := set $target "inspect" $ins }}
{{- end -}}
{{- $file := dict "server" .server "data" "/var/lib/launchway-runner" "secrets" ($r.secretStores | default dict) "deploy" (dict "targets" (dict $r.target $target) "repos" $r.repos) -}}
{{- /* Only when set: a deploy runner of the version before does not know these settings. */ -}}
{{- with include "launchway.shared.healthSources" . | fromYaml }}{{ if . }}{{ $_ := set $file "health_sources" . }}{{ end }}{{ end -}}
{{- if eq (toString $r.ai) "true" }}{{ $_ := set $file "ai" true }}{{ end -}}
{{- /* Only when set: the runner's own limits (accept:), checked on the runner before anything is done. */ -}}
{{- with $r.accept }}{{ $_ := set $file "accept" . }}{{ end -}}
{{- toYaml $file -}}
{{- end -}}

{{/* The deploy runner's health_sources: each verification source by name, with where it is and
how to log in (credentials are secret references only). Empty when none is set. */}}
{{- define "launchway.shared.healthSources" -}}
{{- $out := dict -}}
{{- range (.verification | default dict).sources | default list -}}
{{- $s := dict "type" .type "projects" (.projects | default list) -}}
{{- if .url }}{{ $_ := set $s "url" .url }}{{ end -}}
{{- with .auth -}}
{{- $a := dict "secret" .secret -}}
{{- range $key, $value := dict "type" .type "user" .user "header" .header -}}
{{- if $value }}{{ $_ := set $a $key $value }}{{ end -}}
{{- end -}}
{{- $_ := set $s "auth" $a -}}
{{- end -}}
{{- range $key, $value := dict "headers" .headers "ca_file" .caFile "timeout" .timeout "statements" .statements "indices" .indices "time_field" .timeField "message_field" .messageField "hosts" .hosts "namespaces" .namespaces -}}
{{- if $value }}{{ $_ := set $s $key $value }}{{ end -}}
{{- end -}}
{{- if eq (toString .allowInsecureTLS) "true" }}{{ $_ := set $s "allow_insecure_tls" true }}{{ end -}}
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

{{/*
The deploy runner: its ServiceAccount, ConfigMap, the RBAC in Argo CD's namespace and the
rollout namespaces, the admission policy on Applications, and its Deployment. Its token Secret
is the including chart's business.
*/}}
{{- define "launchway.shared.deployRunner" -}}
{{- $root := .root -}}
{{- $r := .r -}}
{{- $name := .name -}}
{{- $sa := .sa -}}
{{- $component := .component -}}
{{- $config := include "launchway.shared.deployRunnerConfig" . -}}
# The deploy runner: it does the work of deploy and rollback steps for target {{ $r.target }}
# (commit, sync request, observation) with this ServiceAccount, and runs no scripts.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $sa }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
automountServiceAccountToken: false
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
data:
  runner.yaml: |
    # Rendered by the Launchway Helm chart from the deploy runner's values. The pool
    # token comes from a Secret, as an environment variable.
    {{- $config | nindent 4 }}
---
{{- if $r.rbac.create }}
{{- $apps := list }}
{{- range $r.applications }}{{ range .names }}{{ $apps = append $apps . }}{{ end }}{{ end }}
# Read the named Applications (list and watch work with a single-name field selector, which
# is what the deploy runner uses){{ if $r.rbac.patchApplications }} and set their operation and refresh
# annotation (the admission policy keeps the patch to those){{ end }}. Nothing else in {{ $r.argocdNamespace }}.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ $name }}
  namespace: {{ $r.argocdNamespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
rules:
  - apiGroups: [argoproj.io]
    resources: [applications]
    resourceNames: {{ $apps | uniq | toJson }}
    verbs: [get, list, watch{{ if $r.rbac.patchApplications }}, patch{{ end }}]
  # The AppProjects' sync windows, which Launchway never overrides and must read itself.
  - apiGroups: [argoproj.io]
    resources: [appprojects]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ $name }}
  namespace: {{ $r.argocdNamespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: {{ $name }} }
subjects:
  - { kind: ServiceAccount, name: {{ $sa }}, namespace: {{ $root.Release.Namespace }} }
{{- range $ns := $r.rolloutNamespaces }}
---
# Read rollouts and their analysis runs (analysis runs have generated names, so they cannot
# be named here). No pods and no secrets unless rbac.readPods or rbac.inspect say so. The only change: the status of the rollouts named in
# rbac.controlRollouts, if any (promote, abort and the like are status changes).
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ $name }}
  namespace: {{ $ns }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
rules:
  - apiGroups: [argoproj.io]
    resources: [rollouts, analysisruns]
    verbs: [get, list, watch]
  {{- if $r.rbac.readDeployments }}
  - apiGroups: [apps]
    resources: [deployments]
    verbs: [get]
  {{- end }}
  {{- if $r.rbac.readPods }}
  # Pods and their logs, to show why an analysis job failed (rbac.readPods).
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  {{- end }}
  {{- with (index ($r.rbac.controlRollouts | default dict) $ns) }}
  - apiGroups: [argoproj.io]
    resources: [rollouts/status]
    resourceNames: {{ . | toJson }}
    verbs: [patch]
  {{- end }}
  {{- $inspect := $r.rbac.inspect | default dict }}
  {{- if $inspect.read }}
  # Looking into a delivery (rbac.inspect.read): the objects its Launch page shows, read-only, in
  # this namespace only. Secrets are not here (see rbac.inspect.secrets), and nothing can be
  # changed with these rights.
  - apiGroups: [""]
    resources: [pods, services, configmaps, serviceaccounts, persistentvolumeclaims, events]
    verbs: [get, list]
  - apiGroups: [apps]
    resources: [replicasets, deployments, statefulsets, daemonsets]
    verbs: [get, list]
  - apiGroups: [batch]
    resources: [jobs, cronjobs]
    verbs: [get, list]
  - apiGroups: [argoproj.io]
    resources: [experiments]
    verbs: [get, list]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [get, list]
  - apiGroups: [policy]
    resources: [poddisruptionbudgets]
    verbs: [get, list]
  - apiGroups: [networking.k8s.io]
    resources: [ingresses, networkpolicies]
    verbs: [get, list]
  - apiGroups: [gateway.networking.k8s.io]
    resources: [httproutes]
    verbs: [get, list]
  - apiGroups: [networking.istio.io]
    resources: [virtualservices, destinationrules]
    verbs: [get, list]
  - apiGroups: [rbac.authorization.k8s.io]
    resources: [roles, rolebindings]
    verbs: [get, list]
  {{- end }}
  {{- if $inspect.logs }}
  # Logs of the delivery's pods (rbac.inspect.logs): anything a container prints.
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  {{- end }}
  {{- if $inspect.secrets }}
  # The release's Secrets (rbac.inspect.secrets): read so that their values are hidden wherever
  # they turn up in a log line or a ConfigMap. The values stay in the deploy runner's memory and
  # are never sent to the server. This is the broadest right here: leave it off unless needed.
  - apiGroups: [""]
    resources: [secrets]
    verbs: [get, list]
  {{- end }}
  {{- with (index ($inspect.controlWorkloads | default dict) $ns) }}
  # Pause, resume, restart and scale (rbac.inspect.controlWorkloads): patch on exactly these
  # workloads. RBAC cannot limit a patch to some fields, so this lets the deploy runner change
  # their spec: name only workloads whose spec the runner should be able to patch.
  - apiGroups: [argoproj.io]
    resources: [rollouts]
    resourceNames: {{ . | toJson }}
    verbs: [patch]
  - apiGroups: [apps]
    resources: [deployments, statefulsets]
    resourceNames: {{ . | toJson }}
    verbs: [patch]
  {{- end }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ $name }}
  namespace: {{ $ns }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: {{ $name }} }
subjects:
  - { kind: ServiceAccount, name: {{ $sa }}, namespace: {{ $root.Release.Namespace }} }
{{- end }}
---
{{- end }}
{{- if and $r.admissionPolicy.enabled $r.rbac.patchApplications }}
# The patch right on an Application cannot be limited to its operation by RBAC alone, and
# whoever can change an Application's spec controls what Argo CD deploys. This policy lets the
# deploy runner change only .operation and the refresh annotation. Other users, the Argo CD
# controller included, are not matched.
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}-{{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [argoproj.io]
        apiVersions: ["*"]
        operations: [UPDATE]
        resources: [applications]
  matchConditions:
    - name: launchway-deploy-runner
      expression: request.userInfo.username == {{ printf "system:serviceaccount:%s:%s" $root.Release.Namespace $sa | quote }}
  variables:
    - name: refreshKey
      expression: "'argocd.argoproj.io/refresh'"
    - name: newAnnotations
      expression: "has(object.metadata.annotations) ? object.metadata.annotations : {}"
    - name: oldAnnotations
      expression: "has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {}"
  validations:
    - expression: object.spec == oldObject.spec
      message: "Launchway's deploy runner may only request syncs and refreshes; it may not change an Application's spec"
      reason: Forbidden
    - expression: >-
        (has(object.status) ? object.status : {}) == (has(oldObject.status) ? oldObject.status : {})
      message: "Launchway's deploy runner may not change an Application's status"
      reason: Forbidden
    - expression: >-
        (has(object.metadata.labels) ? object.metadata.labels : {}) ==
        (has(oldObject.metadata.labels) ? oldObject.metadata.labels : {})
      message: "Launchway's deploy runner may not change an Application's labels"
      reason: Forbidden
    - expression: >-
        variables.newAnnotations.all(k, k == variables.refreshKey ||
          (k in variables.oldAnnotations && variables.oldAnnotations[k] == variables.newAnnotations[k])) &&
        variables.oldAnnotations.all(k, k == variables.refreshKey || k in variables.newAnnotations)
      message: "Launchway's deploy runner may change only the annotation argocd.argoproj.io/refresh"
      reason: Forbidden
    - expression: >-
        (has(object.metadata.finalizers) ? object.metadata.finalizers : []) ==
        (has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : [])
      message: "Launchway's deploy runner may not change an Application's finalizers"
      reason: Forbidden
    - expression: >-
        (has(object.metadata.ownerReferences) ? object.metadata.ownerReferences : []) ==
        (has(oldObject.metadata.ownerReferences) ? oldObject.metadata.ownerReferences : [])
      message: "Launchway's deploy runner may not change an Application's owner references"
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}-{{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
spec:
  policyName: {{ $name }}-{{ $root.Release.Namespace }}
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ $r.argocdNamespace }}
---
{{- end }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root $component) | nindent 4 }}
spec:
  {{- if not (($r.autoscaling | default dict).enabled) }}
  replicas: {{ $r.replicas }}
  {{- end }}
  revisionHistoryLimit: 5
  selector:
    matchLabels:
      {{- include "launchway.componentSelectorLabels" (list $root $component) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "launchway.componentLabels" (list $root $component) | nindent 8 }}
      annotations:
        checksum/config: {{ $config | sha256sum }}
        {{- include "launchway.shared.scrapeAnnotations" $root | nindent 8 }}
        {{- with $r.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ $sa }}
      # The deploy runner reaches the Kubernetes API with this token.
      automountServiceAccountToken: true
      enableServiceLinks: false
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- range . }}
        - name: {{ . }}
        {{- end }}
      {{- end }}
      securityContext:
        {{- toYaml $root.Values.podSecurityContext | nindent 8 }}
      {{- with $r.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $r.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: deploy-runner
          image: {{ include "launchway.image" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          args:
            - runner
            - --config=/etc/launchway-runner/runner.yaml
            - --health-listen=:{{ $r.healthPort }}
            {{- if (($root.Values.metrics | default dict).enabled) }}
            - --metrics-listen=:{{ $root.Values.metrics.port }}
            {{- end }}
          env:
            {{- include "launchway.shared.metricsEnv" $root | nindent 12 }}
            - name: LAUNCHWAY_RUNNER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ .tokenSecret }}
                  key: {{ .tokenKey }}
            {{- with .env }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- with $r.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          ports:
            - name: health
              containerPort: {{ $r.healthPort }}
              protocol: TCP
            {{- if (($root.Values.metrics | default dict).enabled) }}
            - name: metrics
              containerPort: {{ $root.Values.metrics.port }}
              protocol: TCP
            {{- end }}
          livenessProbe:
            httpGet: { path: /healthz, port: health }
            periodSeconds: 10
          readinessProbe:
            httpGet: { path: /readyz, port: health }
            periodSeconds: 10
          resources:
            {{- toYaml $r.resources | nindent 12 }}
          securityContext:
            {{- toYaml $root.Values.securityContext | nindent 12 }}
          volumeMounts:
            - name: config
              mountPath: /etc/launchway-runner
              readOnly: true
            - name: data
              mountPath: /var/lib/launchway-runner
            - name: tmp
              mountPath: /tmp
            {{- with .mounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- with $r.extraVolumeMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ $name }}
        # Clones of the GitOps repositories; rebuilt after a restart.
        - name: data
          emptyDir:
            sizeLimit: 2Gi
        - name: tmp
          emptyDir:
            sizeLimit: 256Mi
        {{- with .volumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- with $r.extraVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
{{- end -}}

{{/*
The admission policy on the status of the rollouts the deploy runner may control: promote,
promote-full, abort and retry are status changes, and this keeps the patch to the fields those
set (pauseConditions, promoteFull, abort): it cannot, say, change which ReplicaSet is stable.
*/}}
{{- define "launchway.shared.deployRolloutControl" -}}
{{- $root := .root -}}
{{- $r := .r -}}
{{- $name := .name -}}
{{- if and $r.rbac.create $r.admissionPolicy.enabled $r.rbac.controlRollouts }}
# The deploy runner may patch the status of the rollouts in rbac.controlRollouts: promote,
# promote-full, abort and retry are status changes. This policy keeps the patch to the fields
# those set (pauseConditions, promoteFull, abort): it cannot, say, change which ReplicaSet is
# stable. The spec cannot change through the status subresource at all. Other users, the
# Argo Rollouts controller included, are not matched.
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}-rollouts-{{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [argoproj.io]
        apiVersions: ["*"]
        operations: [UPDATE]
        resources: [rollouts, rollouts/status]
  matchConditions:
    - name: launchway-deploy-runner
      expression: request.userInfo.username == {{ printf "system:serviceaccount:%s:%s" $root.Release.Namespace .sa | quote }}
  validations:
    - expression: has(object.status) == has(oldObject.status)
      message: "Launchway's deploy runner may only promote, abort or retry a rollout"
      reason: Forbidden
    {{- range (include "launchway.shared.rolloutStatusFixed" . | splitList " ") }}
    - expression: >-
        !has(object.status) || !has(oldObject.status) ||
        (has(object.status.{{ . }}) == has(oldObject.status.{{ . }}) &&
        (!has(object.status.{{ . }}) || object.status.{{ . }} == oldObject.status.{{ . }}))
      message: "Launchway's deploy runner may not change a rollout's status.{{ . }}"
      reason: Forbidden
    {{- end }}
    - expression: >-
        (has(object.metadata.labels) ? object.metadata.labels : {}) == (has(oldObject.metadata.labels) ? oldObject.metadata.labels : {}) &&
        (has(object.metadata.annotations) ? object.metadata.annotations : {}) == (has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {})
      message: "Launchway's deploy runner may not change a rollout's labels or annotations"
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}-rollouts-{{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
spec:
  policyName: {{ $name }}-rollouts-{{ $root.Release.Namespace }}
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: In
          values: {{ keys $r.rbac.controlRollouts | sortAlpha | toJson }}
{{- end }}
{{- end -}}

{{/* The fields of a rollout's status the deploy runner may not change (Argo Rollouts v1.8
CRD): everything but pauseConditions (promote), promoteFull and abort (abort, retry). */}}
{{- define "launchway.shared.rolloutStatusFixed" -}}
HPAReplicas abortedAt alb albs availableReplicas blueGreen canary collisionCount conditions controllerPause currentPodHash currentStepHash currentStepIndex message observedGeneration phase readyReplicas replicas restartedAt selector stableRS updatedReplicas workloadObservedGeneration
{{- end -}}

{{/* The metrics token and settings a pod gets: the bearer token from a Secret when one is named. */}}
{{- define "launchway.shared.metricsEnv" -}}
{{- $m := .Values.metrics | default dict -}}
{{- if and $m.enabled $m.token $m.token.existingSecret }}
- name: LAUNCHWAY_METRICS_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $m.token.existingSecret }}
      key: {{ $m.token.existingSecretKey | default "token" }}
{{- end }}
{{- end }}

{{/* Annotations for scrapers that find pods by annotation, when metrics.scrapeAnnotations is on. */}}
{{- define "launchway.shared.scrapeAnnotations" -}}
{{- $m := .Values.metrics | default dict -}}
{{- if and $m.enabled $m.scrapeAnnotations }}
prometheus.io/scrape: "true"
prometheus.io/port: {{ $m.port | quote }}
prometheus.io/path: /metrics
{{- end }}
{{- end }}

{{/* A HorizontalPodAutoscaler for the Stations or the deploy runner: resource metrics by default, and any
     other metric (an external or custom one, such as the queue depth) through autoscaling.extraMetrics. */}}
{{- define "launchway.shared.hpa" -}}
{{- $root := .root -}}
{{- $a := .a -}}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "launchway.componentLabels" (list $root .component) | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: {{ .kind | default "Deployment" }}
    name: {{ .name }}
  minReplicas: {{ $a.minReplicas }}
  maxReplicas: {{ $a.maxReplicas }}
  metrics:
    {{- with $a.targetCPUUtilizationPercentage }}
    - type: Resource
      resource:
        name: cpu
        target: { type: Utilization, averageUtilization: {{ . }} }
    {{- end }}
    {{- with $a.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target: { type: Utilization, averageUtilization: {{ . }} }
    {{- end }}
    {{- with $a.extraMetrics }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- with $a.behavior }}
  behavior:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
