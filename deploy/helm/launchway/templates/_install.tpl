{{/* Install-time helpers: where the database is, what the address is, which Secrets the hook Job makes. */}}

{{/* "true" (a non-empty string) when the chart runs its own PostgreSQL: asked for, and no database of your own is named. */}}
{{- define "launchway.bundledDB" -}}
{{- if and .Values.postgresql.enabled (not .Values.database.url) (not .Values.database.existingSecret) -}}true{{- end -}}
{{- end -}}

{{/* externalURL split in scheme and host (fails with a plain message when it is missing or not an address). */}}
{{- define "launchway.externalHost" -}}
{{- if not .Values.externalURL -}}
{{- fail "\n\nexternalURL is required: the address people will open, such as https://launchway.example.com\n  helm install ... --set externalURL=https://launchway.example.com\n" -}}
{{- end -}}
{{- $u := urlParse (toString .Values.externalURL) -}}
{{- if or (not (has $u.scheme (list "http" "https"))) (not $u.host) (not (or (eq $u.path "") (eq $u.path "/"))) -}}
{{- fail (printf "\n\nexternalURL %q must be an address with a scheme and a host and no path, such as https://launchway.example.com\n" (toString .Values.externalURL)) -}}
{{- end -}}
{{- $u.host -}}
{{- end -}}

{{/* The host name part of externalURL, without a port. */}}
{{- define "launchway.externalHostname" -}}
{{- regexReplaceAll ":[0-9]+$" (include "launchway.externalHost" .) "" -}}
{{- end -}}

{{/* The sign-in mode the servers run with. */}}
{{- define "launchway.authMode" -}}
{{- (get (.Values.config.extraConfig | default dict) "auth" | default dict).mode | default "none" -}}
{{- end -}}

{{/* A Kubernetes quantity of storage in MiB (Ki, Mi, Gi, Ti, K, M, G, T, or plain bytes). */}}
{{- define "launchway.quantityMiB" -}}
{{- $q := toString . -}}
{{- $n := regexFind "^[0-9]+(\\.[0-9]+)?" $q | float64 -}}
{{- $unit := regexFind "[A-Za-z]+$" $q -}}
{{- $mult := dict "" (divf 1 1048576) "Ki" (divf 1 1024) "Mi" 1.0 "Gi" 1024.0 "Ti" 1048576.0 "K" (divf 1000 1048576) "M" (divf 1000000 1048576) "G" (divf 1000000000 1048576) "T" (divf 1000000000000 1048576) -}}
{{- if not (hasKey $mult $unit) -}}{{ fail (printf "storage size %q is not a quantity the chart can compare (use Mi, Gi or Ti)" $q) }}{{- end -}}
{{- int64 (mulf $n (get $mult $unit)) -}}
{{- end -}}

{{/* With the bundled database: the artifact cap (GiB) derived from its volume, half of it. */}}
{{- define "launchway.derivedArtifactCapGiB" -}}
{{- $vol := include "launchway.quantityMiB" .Values.postgresql.persistence.size | int64 -}}
{{- $cap := div $vol 2048 -}}
{{- if lt $cap 1 -}}
{{- fail (printf "\n\npostgresql.persistence.size %s is too small to keep artifacts in the database (half of it must be at least 1Gi). Use 2Gi or more, or keep artifacts elsewhere with config.artifacts.store (s3 or filesystem).\n" .Values.postgresql.persistence.size) -}}
{{- end -}}
{{- $cap -}}
{{- end -}}

{{/* Storage coherence: with artifacts in the bundled database, the cap must leave room on the volume. */}}
{{- define "launchway.validateStorage" -}}
{{- $a := .Values.config.artifacts | default dict -}}
{{- if and (include "launchway.bundledDB" . | trim) (has (toString $a.store) (list "" "database" "<nil>")) -}}
{{- $cap := (($a.database | default dict).maxTotalGiB) -}}
{{- if $cap -}}
{{- $vol := include "launchway.quantityMiB" .Values.postgresql.persistence.size | int64 -}}
{{- if gt (mul (int64 $cap) 1024) (div (mul $vol 3) 4) -}}
{{- fail (printf "\n\nconfig.artifacts.database.maxTotalGiB is %v GiB but the bundled database's volume (postgresql.persistence.size) is %s: artifacts live in the database, so the cap must stay under three quarters of the volume (the database and its logs need the rest). Lower the cap, enlarge postgresql.persistence.size, or leave the cap unset (the chart then uses half the volume).\n" $cap .Values.postgresql.persistence.size) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The servers' database environment. The bundled database's URL is built from its password Secret
(Kubernetes expands $(VAR) from variables listed before); the password is letters and digits only. */}}
{{- define "launchway.databaseEnv" -}}
{{- if include "launchway.bundledDB" . | trim }}
- name: LAUNCHWAY_DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "launchway.postgresqlName" . }}
      key: password
- name: LAUNCHWAY_DATABASE
  value: {{ printf "postgres://%s:$(LAUNCHWAY_DB_PASSWORD)@%s.%s.svc:5432/%s?sslmode=disable" (.Values.postgresql.username | urlquery) (include "launchway.postgresqlName" .) .Release.Namespace .Values.postgresql.database | quote }}
{{- else }}
- name: LAUNCHWAY_DATABASE
  valueFrom:
    secretKeyRef:
      name: {{ include "launchway.databaseSecretName" . }}
      key: {{ include "launchway.databaseSecretKey" . }}
{{- end }}
{{- end -}}

{{- define "launchway.setupCodeSecretName" -}}
{{- printf "%s-setup-code" (include "launchway.fullname" .) -}}
{{- end -}}

{{/* The Secrets the install hook Job makes, as "flag=name" lines (empty: nothing to make, no Job). */}}
{{- define "launchway.bootstrapSecrets" -}}
{{- $h := include "launchway.encryptionHolder" . -}}
{{- if and (eq $h "keyFile") (not .Values.encryption.keyFile.existingSecret) -}}
{{- if not .Values.encryption.keyFile.generate }}{{ fail "encryption.keyFile.generate is false: set encryption.keyFile.existingSecret to the Secret that holds the root key" }}{{ end }}
root-key={{ include "launchway.rootKeySecretName" . }}
{{ end -}}
{{- if not .Values.runnerToken.existingSecret }}
runner-token={{ include "launchway.runnerTokenSecretName" . }}
{{ end -}}
{{- if and (include "launchway.bundledDB" . | trim) (not .Values.postgresql.password) }}
db-password={{ include "launchway.postgresqlName" . }}
{{ end -}}
{{- if and .Values.delivery.deployRunner.enabled (not .Values.delivery.deployRunner.poolToken.existingSecret) }}
deploy-pool-token={{ include "launchway.deployPoolSecretName" . }}
{{ end -}}
{{- if eq (include "launchway.authMode" .) "local" }}
setup-code={{ include "launchway.setupCodeSecretName" . }}
{{ end -}}
{{- end -}}
