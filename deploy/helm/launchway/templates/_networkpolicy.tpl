{{/* DNS egress rule for the pods of the policies. */}}
{{- define "launchway.dnsEgress" -}}
{{- $np := .Values.networkPolicy -}}
{{- if $np.dns.enabled -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ $np.dns.namespace }}
      podSelector:
        matchLabels:
          {{- toYaml $np.dns.podLabels | nindent 10 }}
  ports:
    - { protocol: UDP, port: 53 }
    - { protocol: TCP, port: 53 }
{{- end -}}
{{- end -}}
