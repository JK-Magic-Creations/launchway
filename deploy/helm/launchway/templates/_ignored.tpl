{{/* Values this chart version does not know. The values schema is open on purpose (a closed one
     refuses a values file written for a newer chart, which made a rollback impossible), so a typo
     is found here instead: known-values.json is generated from values.schema.json
     (internal/config TestKnownValuesCurrent keeps it current). Unknown keys do not stop the render
     unless strictValues is true; they are listed in the NOTES, in the server's environment and in an
     annotation, and the server and `launchway doctor` say so. `global` is Helm's own. */}}
{{- define "launchway.unknownWalk" -}}
{{- $vals := index . 0 -}}{{- $known := index . 1 -}}{{- $path := index . 2 -}}
{{- range $k, $v := $vals -}}
{{- $p := ternary $k (printf "%s.%s" $path $k) (eq $path "") -}}
{{- if not (hasKey $known $k) }}
{{ $p }}
{{- else -}}
{{- $kn := index $known $k -}}
{{- if and (kindIs "map" $kn) (kindIs "map" $v) }}{{ include "launchway.unknownWalk" (list $v $kn $p) }}{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* A comma-separated list of the ignored keys, empty when there are none. */}}
{{- define "launchway.ignoredValues" -}}
{{- $known := .Files.Get "known-values.json" | fromJson -}}
{{- $out := list -}}
{{- range (include "launchway.unknownWalk" (list (omit .Values "global") $known "") | splitList "\n" | compact | sortAlpha) -}}
{{- $out = append $out . -}}
{{- end -}}
{{- join "," $out -}}
{{- end -}}

{{/* Fails with the list when strictValues is true. Called from validate. */}}
{{- define "launchway.validateKnownValues" -}}
{{- $ign := include "launchway.ignoredValues" . -}}
{{- if and $ign .Values.strictValues -}}
{{- fail (printf "\n\nstrictValues is true and this chart (%s, app %s) does not know these values: %s\nA typo, or a setting for a newer chart. Fix the name, or set strictValues: false to ignore them (they are then listed in the NOTES and reported by the server and `launchway doctor`).\n" .Chart.Version .Chart.AppVersion (replace "," ", " $ign)) -}}
{{- end -}}
{{- end -}}

{{/* THE IMAGE AND THE CHART MAY ARRIVE SEPARATELY (a GitOps controller, a rollback). The chart declares
     the range of app versions it supports (annotation launchway.io/supported-app-versions) and the
     first version whose binary has `launchway bootstrap-secrets` and reads the mounted root key
     (launchway.io/bootstrap-since). The version is read from image.tag (the chart's appVersion when
     empty). The image has no shell, so the Job cannot probe the binary: the chart decides here.
     A tag that is not a version (a commit, "latest", a digest alone) is taken to be current; set
     image.bootstrap to "true" or "false" to say so. */}}
{{- define "launchway.imageVersion" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion | toString | trimPrefix "v" -}}
{{- regexFind "^[0-9]+\\.[0-9]+\\.[0-9]+" $tag -}}
{{- end -}}

{{/* "true" or "false": does the image know the secrets bootstrap and the mounted root key? */}}
{{- define "launchway.imageHasBootstrap" -}}
{{- $v := include "launchway.imageVersion" . -}}
{{- $since := index .Chart.Annotations "launchway.io/bootstrap-since" -}}
{{- if eq (toString .Values.image.bootstrap) "true" -}}true
{{- else if eq (toString .Values.image.bootstrap) "false" -}}false
{{- else if and $v (semverCompare (printf "< %s" $since) $v) -}}false
{{- else -}}true
{{- end -}}
{{- end -}}

{{- define "launchway.validateImageVersion" -}}
{{- $v := include "launchway.imageVersion" . -}}
{{- $range := index .Chart.Annotations "launchway.io/supported-app-versions" -}}
{{- if and $v (not (semverCompare $range $v)) -}}
{{- fail (printf "\n\nThis chart (%s) supports Launchway images %s, and image.tag is %s.\nDeploy a chart that belongs to that image, or set image.tag to a version this chart supports (see the upgrade guide, \"The compatibility rule\").\n" .Chart.Version $range .Values.image.tag) -}}
{{- end -}}
{{- end -}}
