#!/bin/sh
# Runtime branding for the standalone (non-Kubernetes) frontend image.
#
# Installed into /docker-entrypoint.d/ so the stock nginx image runs it before
# starting nginx. In Kubernetes the Helm chart renders /config.json into a
# ConfigMap and mounts it read-only over the file baked into the image, so
# branding is fully handled there and this script leaves that file alone (it
# only writes when a BRANDING_* / KEYCLOAK_* / BRANDING_CONFIG_FILE env var is
# set, which the chart never sets). Outside Kubernetes it lets an operator
# rebrand without rebuilding:
#
#   Precedence (highest first), resolved per field:
#     1. A mounted /config.json (or BRANDING_CONFIG_FILE) — a full local file
#     2. Individual BRANDING_* / KEYCLOAK_* env vars overlaid on the base file
#     3. The placeholder config.json baked into the image
#     4. Built-in Nebari defaults (applied by the SPA when a field is empty)
#
# Supported env vars:
#   BRANDING_CONFIG_FILE   path to a JSON file used as the base config.json
#   BRANDING_TITLE         browser-tab title
#   BRANDING_LOGO_URL      header logo URL (light / default)
#   BRANDING_LOGO_URL_DARK dark-mode header logo URL
#   BRANDING_FAVICON_URL   favicon URL
#   BRANDING_THEME         raw JSON theme object, e.g.
#                          '{"light":{"primary":"#0066cc"},"dark":{}}'
#   KEYCLOAK_URL / KEYCLOAK_REALM / KEYCLOAK_CLIENT_ID   Keycloak overrides
#
# The script never aborts container startup: any failure to build or write the
# config is logged and the existing file is kept. It always exits 0.
set -u

CONFIG_PATH="${CONFIG_PATH:-/usr/share/nginx/html/config.json}"

# Do nothing unless the operator asked for a runtime override. This keeps the
# read-only Kubernetes mount (and read-only root filesystem) untouched.
if [ -z "${BRANDING_CONFIG_FILE:-}" ] \
  && [ -z "${BRANDING_TITLE:-}" ] \
  && [ -z "${BRANDING_LOGO_URL:-}" ] \
  && [ -z "${BRANDING_LOGO_URL_DARK:-}" ] \
  && [ -z "${BRANDING_FAVICON_URL:-}" ] \
  && [ -z "${BRANDING_THEME:-}" ] \
  && [ -z "${KEYCLOAK_URL:-}" ] \
  && [ -z "${KEYCLOAK_REALM:-}" ] \
  && [ -z "${KEYCLOAK_CLIENT_ID:-}" ]; then
  exit 0
fi

base="$CONFIG_PATH"
if [ -n "${BRANDING_CONFIG_FILE:-}" ]; then
  if [ -r "$BRANDING_CONFIG_FILE" ]; then
    base="$BRANDING_CONFIG_FILE"
  else
    echo "branding: BRANDING_CONFIG_FILE=$BRANDING_CONFIG_FILE not readable; ignoring" >&2
  fi
fi

if [ ! -r "$base" ]; then
  echo "branding: no readable base config at $base; skipping" >&2
  exit 0
fi

# Build a jq program that sets only the fields whose env var is provided.
# Scalars come in as strings via --arg; the theme object via --argjson.
filter='.'
set --  # positional args accumulate the jq --arg / --argjson pairs
if [ -n "${KEYCLOAK_URL:-}" ]; then
  filter="$filter | .keycloak.url = \$kcUrl"; set -- "$@" --arg kcUrl "$KEYCLOAK_URL"
fi
if [ -n "${KEYCLOAK_REALM:-}" ]; then
  filter="$filter | .keycloak.realm = \$kcRealm"; set -- "$@" --arg kcRealm "$KEYCLOAK_REALM"
fi
if [ -n "${KEYCLOAK_CLIENT_ID:-}" ]; then
  filter="$filter | .keycloak.clientId = \$kcClient"; set -- "$@" --arg kcClient "$KEYCLOAK_CLIENT_ID"
fi
if [ -n "${BRANDING_TITLE:-}" ]; then
  filter="$filter | .title = \$title"; set -- "$@" --arg title "$BRANDING_TITLE"
fi
if [ -n "${BRANDING_LOGO_URL:-}" ]; then
  filter="$filter | .logoUrl = \$logo"; set -- "$@" --arg logo "$BRANDING_LOGO_URL"
fi
if [ -n "${BRANDING_LOGO_URL_DARK:-}" ]; then
  filter="$filter | .logoUrlDark = \$logoDark"; set -- "$@" --arg logoDark "$BRANDING_LOGO_URL_DARK"
fi
if [ -n "${BRANDING_FAVICON_URL:-}" ]; then
  filter="$filter | .faviconUrl = \$favicon"; set -- "$@" --arg favicon "$BRANDING_FAVICON_URL"
fi
if [ -n "${BRANDING_THEME:-}" ]; then
  filter="$filter | .theme = \$theme"; set -- "$@" --argjson theme "$BRANDING_THEME"
fi

tmp="$(mktemp)"
if jq "$@" "$filter" "$base" > "$tmp" 2>/dev/null; then
  if cat "$tmp" > "$CONFIG_PATH" 2>/dev/null; then
    echo "branding: applied runtime branding to $CONFIG_PATH" >&2
  else
    echo "branding: could not write $CONFIG_PATH (read-only?); keeping existing config" >&2
  fi
else
  echo "branding: failed to build config (invalid BRANDING_THEME JSON?); keeping existing config" >&2
fi
rm -f "$tmp"

exit 0
