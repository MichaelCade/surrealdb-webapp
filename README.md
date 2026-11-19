# SurrealDB multi-model shop — a Kasten protection demo

A small Go web app that shows SurrealDB's data models side by side, with a **health panel** that
turns red when the data is damaged and green again after a restore. It exists to demo protecting
SurrealDB with Kasten, using the keeper-PVC blueprint from
[kastenhq/blueprint-ai](https://github.com/kastenhq/blueprint-ai) (`surrealdb-example`).

It is a demo, not a template for a production application.

| Panel | SurrealDB model | What it shows |
|---|---|---|
| Catalogue | Relational, document | A schemafull `product` table with a unique index; schemaless `customer` records; spend from a stored function |
| Who bought what | Graph | `customer->ordered->product` edges; click a product for "customers who bought this also bought" |
| Review search | Full-text | BM25 search with highlights over product reviews |
| Similar products | Vector | A 6-number embedding per product, searched with an HNSW index and cosine distance |
| Nearest store | Geospatial | Store and customer points, nearest store by `geo::distance` |
| Live orders | Time series, live query | ULID-keyed order events, orders per minute, and a feed pushed by a SurrealDB live query |
| **Health** | All of the above | One check per model against the seeded data |

The traffic generator only adds new records, so it never turns a check red. **Simulate incident**
damages every model at once: it rewrites prices, deletes a customer and its orders, drops the
review table and the vector index, deletes a store and the seeded order events.

## Architecture

```
 browser ── http://shop.<ip>.nip.io ──► surrealdb-webapp (Go) ── ws://surrealdb:8000 ──► SurrealDB 3.3.5
                                                                                        ▲
 SurrealDB Studio ── http://surrealdb.<ip>.nip.io ─────────────────────────────────────┘
```

Everything runs in the `surrealdb-test` namespace, so one Kasten restore brings back the app and
its data together. The app connects as a database-level `EDITOR` user on `demo/shop`, never as root.

## Prerequisites

- SurrealDB 3.3.5, the keeper, the blueprint and the Kasten policy from the blueprint-ai
  `surrealdb-example/3.3` runbook, in namespace `surrealdb-test`.
- An ingress controller. The manifests use Traefik and `nip.io` hostnames on
  `192.168.169.190`, the Traefik load balancer IP of the test cluster: **replace that IP** with yours.

## Deploy

```bash
# 1. Namespace demo, database shop, and the app's EDITOR user (password generated into Secret surrealdb-demo)
sh deploy/setup-db.sh

# 2. The app: Deployment, Service and Ingress (shop.<ip>.nip.io). Set the image tag first.
kubectl apply -f deploy/webapp.yaml

# 3. Optional: a route for SurrealDB Studio (surrealdb.<ip>.nip.io)
kubectl apply -f deploy/surrealdb-ingress.yaml
```

The app seeds `demo/shop` on its first start, and re-applies the schema on every start (every
statement is `IF NOT EXISTS` or `OVERWRITE`).

The image is built by [.github/workflows/image.yaml](.github/workflows/image.yaml) on every push to
`main` and published as `ghcr.io/michaelcade/surrealdb-webapp:sha-<commit>` (use this in
`deploy/webapp.yaml`) and `:latest`.

> **Plain HTTP.** Both routes use HTTP on a home-lab LAN, like the other routes on the test
> cluster. Studio's root password crosses the network unencrypted. Add TLS before using this
> anywhere you do not control the network.

## Connect SurrealDB Studio

Use the **desktop** app (download it from [surrealdb.com/studio](https://surrealdb.com/studio)).
The browser version is served over HTTPS and browsers block it from opening a plain `ws://`
connection to another host.

| Field | Value |
|---|---|
| Endpoint | `http://surrealdb.<ip>.nip.io` |
| Authentication | Root, user `root` |
| Password | `kubectl get secret -n surrealdb-test surrealdb-root -o jsonpath='{.data.password}' \| base64 -d \| pbcopy` |

Then select namespace `demo`, database `shop`.

## Demo script (about 10 minutes)

1. **Tour.** Open `http://shop.<ip>.nip.io`. Walk through the panels; the health panel shows 7 of 7.
   Tick **Generate orders**: the live feed and the chart start moving.
2. **Back up while writing.** In Kasten, run the `surrealdb-test-backup` policy. Point out that
   the orders keep flowing: the blueprint's `backupPrehook` runs `surreal export`, which reads each
   database in one transaction, so nothing is paused.
3. **Incident.** Click **Simulate incident**. The health panel goes to 0 of 7, the search panel
   finds nothing, the vector panel loses its index, a store disappears.
4. **Restore.** In Kasten, restore the namespace from the **exported** restore point. Kasten
   restores both PVCs, restarts the pods, then the `restorePosthook` removes and re-imports every
   database from the consistent dump. The app reconnects by itself.
5. **Verify.** The health panel is back to 7 of 7. Optionally show the graph in Studio.

Validated on Kubernetes 1.34.1 (Talos 1.12.6), Kasten 9.0.8, SurrealDB 3.3.5, Rook Ceph RBD: 7 of
7 before backup, 0 of 7 after the incident, 7 of 7 after a restore from the exported restore point.

## Run locally

```bash
SURREAL_URL=ws://surrealdb.<ip>.nip.io \
SURREAL_PASS="$(kubectl get secret -n surrealdb-test surrealdb-demo -o jsonpath='{.data.password}' | base64 -d)" \
LISTEN_ADDR=127.0.0.1:8088 go run .
```

| Variable | Default | Meaning |
|---|---|---|
| `SURREAL_URL` | `ws://surrealdb:8000` | WebSocket endpoint (live queries need WebSocket) |
| `SURREAL_NS` / `SURREAL_DB` | `demo` / `shop` | Namespace and database |
| `SURREAL_USER` / `SURREAL_PASS` | `demo` / — | Database-level user |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |

## SurrealDB 3 notes found while building this

- A function call is not accepted in `ORDER BY`: select it as a field and order by the field.
- Whole numbers come back as integers; cast with `<float>` when decoding into Go `float64`.
- In a KNN query (`<|k,ef|>`), a `WHERE id != …` filter is applied **before** the `k` limit.
- `surreal sql` reads piped input one line at a time; the SDK takes multi-line queries fine.
