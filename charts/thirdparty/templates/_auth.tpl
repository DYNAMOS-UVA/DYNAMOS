{{/*
DYNAMOS auth helpers. The same file is in every chart that runs a service
with pkg/auth (orchestrator, api-gateway, agents, thirdparty,
transfer-process-service, dsp-connector, negotiation-service). Change all
copies together.

Values:

  auth:
    secretName: dynamos-auth      Secret from scripts/auth-secrets.sh,
                                  mounted read-only at /etc/dynamos/auth
    env: {}                       pkg/auth environment (AUTH_*, TOKEN_ISSUER_*).
                                  Empty values are not set.
    devToken:                     optional, development only
      enabled: false              accept the static-dev token from the Secret.
                                  Needs an image built with -tags authdev.
      secretKey: dev-user-token   key in the Secret that holds the token
      subject: ...                Principal.Subject for the token
      kind: user                  user, service or participant
      scopes: ""                  comma-separated
  corsAllowedOrigins: ""          CORS_ALLOWED_ORIGINS (empty: no CORS)
*/}}

{{- define "dynamos.authEnv" -}}
{{- $auth := .Values.auth | default dict -}}
{{- $env := deepCopy ($auth.env | default dict) -}}
{{- $dev := $auth.devToken | default dict -}}
{{- if $dev.enabled -}}
{{- $providers := get $env "AUTH_PROVIDERS" | default "" | toString -}}
{{- $_ := set $env "AUTH_PROVIDERS" (ternary "static-dev" (printf "%s,static-dev" $providers) (eq $providers "")) -}}
{{- $_ := set $env "AUTH_STATIC_DEV_SUBJECT" (required "auth.devToken.subject is required" $dev.subject) -}}
{{- $_ := set $env "AUTH_STATIC_DEV_KIND" ($dev.kind | default "user") -}}
{{- $_ := set $env "AUTH_STATIC_DEV_SCOPES" ($dev.scopes | default "") -}}
{{- end -}}
{{- range $name, $value := $env }}
{{- if ne (toString $value) "" }}
- name: {{ $name }}
  value: {{ toString $value | quote }}
{{- end }}
{{- end }}
{{- if $dev.enabled }}
- name: AUTH_STATIC_DEV_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $auth.secretName }}
      key: {{ $dev.secretKey | default "dev-user-token" }}
{{- end }}
{{- with .Values.corsAllowedOrigins }}
- name: CORS_ALLOWED_ORIGINS
  value: {{ . | quote }}
{{- end }}
{{- end -}}

{{- define "dynamos.authVolumeMount" -}}
- name: dynamos-auth
  mountPath: /etc/dynamos/auth
  readOnly: true
{{- end -}}

{{- define "dynamos.authVolume" -}}
- name: dynamos-auth
  secret:
    secretName: {{ .Values.auth.secretName }}
    defaultMode: 0400
{{- end -}}
