#!/bin/sh
# Creates the demo namespace, database and the app's database user.
# Run once, after SurrealDB is deployed (see the blueprint-ai surrealdb-example runbook).
#
# - The app user is an EDITOR on demo/shop only: it can define schema and write data,
#   but cannot create users or touch other databases.
# - Its password is generated into the Secret surrealdb-demo and never printed.
set -eu

NS=${NAMESPACE:-surrealdb-test}
IMAGE=${SURREAL_IMAGE:-surrealdb/surrealdb:v3.3.5-dev}

if ! kubectl get secret -n "$NS" surrealdb-demo >/dev/null 2>&1; then
  kubectl create secret generic surrealdb-demo -n "$NS" \
    --from-literal=username=demo --from-literal=password="$(openssl rand -hex 16)"
fi

# A throwaway pod with the root and demo credentials in its environment. The statements
# are on separate lines because `surreal sql` reads standard input one line at a time.
kubectl run surrealdb-demo-setup -n "$NS" --rm -i --restart=Never --image="$IMAGE" \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"setup","image":"'"$IMAGE"'","stdin":true,"command":["sh","-c","printf \"DEFINE NAMESPACE IF NOT EXISTS demo;\\nUSE NS demo;\\nDEFINE DATABASE IF NOT EXISTS shop;\\nUSE NS demo DB shop;\\nDEFINE USER OVERWRITE demo ON DATABASE PASSWORD \\\"$DEMO_PASS\\\" ROLES EDITOR;\\n\" | /surreal sql -e http://surrealdb:8000 -u \"$ROOT_USER\" -p \"$ROOT_PASS\" --hide-welcome --json"],"env":[{"name":"ROOT_USER","valueFrom":{"secretKeyRef":{"name":"surrealdb-root","key":"username"}}},{"name":"ROOT_PASS","valueFrom":{"secretKeyRef":{"name":"surrealdb-root","key":"password"}}},{"name":"DEMO_PASS","valueFrom":{"secretKeyRef":{"name":"surrealdb-demo","key":"password"}}}],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}'
