#!/usr/bin/env bash
# Make sure alice (team-a) and bob (team-b) exist in the dev Keycloak's nebari
# realm, in the llm-users group and their team's group, with the passwords
# kept in the Secret keycloak/demo-users (random, made on the first run).
#
# The dev Keycloak runs start-dev, so a restart of its pod forgets them;
# re-run this afterwards. Used by usage-e2e.sh and the usage tracer.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
# Pinned to the kind cluster's context so this never acts on another cluster.
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

# Random passwords, made once and kept in the Secret keycloak/demo-users.
if ! $KUBECTL -n keycloak get secret demo-users >/dev/null 2>&1; then
  $KUBECTL -n keycloak create secret generic demo-users \
    --from-literal=alice="$(openssl rand -base64 24)" --from-literal=bob="$(openssl rand -base64 24)" >/dev/null
fi
userpw() { $KUBECTL -n keycloak get secret demo-users -o jsonpath="{.data.$1}" | base64 -d; }
admin_pw="$($KUBECTL -n keycloak get deploy keycloak -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="KC_BOOTSTRAP_ADMIN_PASSWORD")].value}')"
# Passwords go over stdin, never on a command line.
{ printf '%s\n%s\n%s\n' "$admin_pw" "$(userpw alice)" "$(userpw bob)"; } | $KUBECTL -n keycloak exec -i deploy/keycloak -- bash -c '
set -e
read -r ADMIN_PW; read -r ALICE_PW; read -r BOB_PW
KC=/opt/keycloak/bin/kcadm.sh
$KC config credentials --server http://localhost:8080 --realm master --user admin --password "$ADMIN_PW" >/dev/null
for g in llm-users team-a team-b; do
  [ -n "$($KC get groups -r nebari -q search=$g --fields name 2>/dev/null | grep "\"$g\"")" ] || $KC create groups -r nebari -s name=$g >/dev/null
done
user() { # name team password
  if [ -z "$($KC get users -r nebari -q username=$1 -q exact=true --fields id | grep id)" ]; then
    $KC create users -r nebari -s username=$1 -s enabled=true -s email=$1@example.com -s emailVerified=true -s firstName=$1 -s lastName=demo >/dev/null
  fi
  $KC set-password -r nebari --username $1 --new-password "$3" >/dev/null
  id=$($KC get users -r nebari -q username=$1 -q exact=true --fields id | sed -n "s/.*\"id\" *: *\"\([^\"]*\)\".*/\1/p")
  for g in llm-users $2; do
    gid=$($KC get groups -r nebari -q search=$g -q exact=true --fields id,name | grep -B1 "\"$g\"" | sed -n "s/.*\"id\" *: *\"\([^\"]*\)\".*/\1/p")
    $KC update users/$id/groups/$gid -r nebari -s realm=nebari -s userId=$id -s groupId=$gid -n >/dev/null
  done
}
user alice team-a "$ALICE_PW"
user bob team-b "$BOB_PW"
'
unset admin_pw
