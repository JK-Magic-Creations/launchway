{{/* Encryption at rest: where the root key is held (values: encryption.*). Environment variables
     only, so the configuration file is unchanged and the release before can still read it. */}}

{{- define "launchway.encryptionHolder" -}}
{{- $h := .Values.encryption.holder | default "keyFile" -}}
{{- if not (has $h (list "keyFile" "env" "passphrase" "transit" "none")) -}}
{{- fail (printf "encryption.holder %q is not known: use keyFile, env, passphrase, transit or none" $h) -}}
{{- end -}}
{{- $h -}}
{{- end -}}

{{- define "launchway.rootKeySecretName" -}}
{{- .Values.encryption.keyFile.existingSecret | default (printf "%s-root-key" (include "launchway.fullname" .)) -}}
{{- end -}}

{{- define "launchway.rootKeySecretKey" -}}
{{- if .Values.encryption.keyFile.existingSecret -}}{{ .Values.encryption.keyFile.existingSecretKey }}{{- else -}}root.key{{- end -}}
{{- end -}}

{{/* The server pods' environment for the root key holder. */}}
{{- define "launchway.encryptionEnv" -}}
{{- $e := .Values.encryption -}}
- name: LAUNCHWAY_CHART_REVISION
  value: {{ index .Chart.Annotations "launchway.io/chart-revision" | quote }}
{{- if .Values.networkPolicy.enabled }}
- name: LAUNCHWAY_NETWORK_POLICY
  value: chart
{{- end }}
{{- with include "launchway.ignoredValues" . }}
- name: LAUNCHWAY_IGNORED_VALUES
  value: {{ . | quote }}
{{- end }}
{{- $h := include "launchway.encryptionHolder" . -}}
{{- /* An image from before encryption at rest reads no key: nothing more is passed to it. */ -}}
{{- if eq (include "launchway.imageHasBootstrap" .) "true" }}
{{- if eq $h "none" }}
- name: LAUNCHWAY_ENCRYPTION_HOLDER
  value: ""
{{- else }}
- name: LAUNCHWAY_ENCRYPTION_REQUIRE
  value: "true"
{{- end }}
{{- if eq $h "keyFile" }}
- name: LAUNCHWAY_ROOT_KEY_FILE
  value: /etc/launchway-keys/root.key
{{- else if eq $h "env" }}
{{- if not $e.env.existingSecret }}{{ fail "encryption.holder is env: set encryption.env.existingSecret" }}{{ end }}
- name: LAUNCHWAY_ROOT_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $e.env.existingSecret }}
      key: {{ $e.env.existingSecretKey }}
{{- else if eq $h "passphrase" }}
{{- if not (and $e.passphrase.keystoreSecret $e.passphrase.passphraseSecret) }}{{ fail "encryption.holder is passphrase: set encryption.passphrase.keystoreSecret and passphraseSecret" }}{{ end }}
- name: LAUNCHWAY_ENCRYPTION_HOLDER
  value: passphrase
- name: LAUNCHWAY_KEYSTORE_FILE
  value: /etc/launchway-keys/root.keystore
- name: LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE
  value: /etc/launchway-keys/passphrase
{{- else if eq $h "transit" }}
{{- $t := $e.transit }}
{{- if not (and $t.address $t.key) }}{{ fail "encryption.holder is transit: set encryption.transit.address and key" }}{{ end }}
{{- if not (or $t.tokenSecret $t.role) }}{{ fail "encryption.holder is transit: set encryption.transit.tokenSecret, or role for Kubernetes auth" }}{{ end }}
- name: LAUNCHWAY_ENCRYPTION_HOLDER
  value: transit
- name: LAUNCHWAY_TRANSIT_ADDRESS
  value: {{ $t.address | quote }}
- name: LAUNCHWAY_TRANSIT_MOUNT
  value: {{ $t.mount | default "transit" | quote }}
- name: LAUNCHWAY_TRANSIT_KEY
  value: {{ $t.key | quote }}
{{- with $t.namespace }}
- name: LAUNCHWAY_TRANSIT_NAMESPACE
  value: {{ . | quote }}
{{- end }}
{{- if $t.caSecret }}
- name: LAUNCHWAY_TRANSIT_CA_FILE
  value: /etc/launchway-keys/ca.crt
{{- end }}
{{- if $t.tokenSecret }}
- name: LAUNCHWAY_TRANSIT_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $t.tokenSecret }}
      key: {{ $t.tokenSecretKey }}
- name: LAUNCHWAY_TRANSIT_TOKEN_ENV
  value: LAUNCHWAY_TRANSIT_TOKEN
{{- else }}
- name: LAUNCHWAY_TRANSIT_ROLE
  value: {{ $t.role | quote }}
- name: LAUNCHWAY_TRANSIT_AUTH_PATH
  value: {{ $t.authPath | default "kubernetes" | quote }}
{{- end }}
{{- end }}
{{- if $e.previousKey.existingSecret }}
- name: LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE
  value: /etc/launchway-keys/previous.key
{{- end }}
{{- end }}
{{- end -}}

{{/* The sources of the projected volume holding the key files (empty: no volume is needed). */}}
{{- define "launchway.encryptionSources" -}}
{{- $e := .Values.encryption -}}
{{- $h := include "launchway.encryptionHolder" . -}}
{{- if ne (include "launchway.imageHasBootstrap" .) "true" }}
{{- else if eq $h "keyFile" }}
- secret:
    name: {{ include "launchway.rootKeySecretName" . }}
    items:
      - key: {{ include "launchway.rootKeySecretKey" . }}
        path: root.key
{{- else if eq $h "passphrase" }}
- secret:
    name: {{ $e.passphrase.keystoreSecret }}
    items:
      - key: {{ $e.passphrase.keystoreSecretKey }}
        path: root.keystore
- secret:
    name: {{ $e.passphrase.passphraseSecret }}
    items:
      - key: {{ $e.passphrase.passphraseSecretKey }}
        path: passphrase
{{- else if and (eq $h "transit") $e.transit.caSecret }}
- secret:
    name: {{ $e.transit.caSecret }}
    items:
      - key: ca.crt
        path: ca.crt
{{- end }}
{{- if $e.previousKey.existingSecret }}
- secret:
    name: {{ $e.previousKey.existingSecret }}
    items:
      - key: {{ $e.previousKey.existingSecretKey }}
        path: previous.key
{{- end }}
{{- end -}}
