package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
)

type api struct{ store *store }

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/catalogue", a.catalogue)
	mux.HandleFunc("GET /api/graph", a.graph)
	mux.HandleFunc("GET /api/recommend", a.recommend)
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("GET /api/similar", a.similar)
	mux.HandleFunc("GET /api/stores", a.stores)
	mux.HandleFunc("GET /api/orders", a.orders)
	mux.HandleFunc("GET /api/orders/live", a.live)
	mux.HandleFunc("POST /api/traffic", a.traffic)
	mux.HandleFunc("POST /api/incident", a.incident)
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (a *api) status(w http.ResponseWriter, r *http.Request) {
	connected, detail := a.store.status()
	writeJSON(w, map[string]any{"connected": connected, "detail": detail, "traffic": a.store.trafficRunning()}, nil)
}

// ---- document + relational ----

type product struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Price    float64 `json:"price"`
}

type customer struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	City  string  `json:"city"`
	Total float64 `json:"total"`
}

func (a *api) catalogue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	products, err := one[[]product](ctx, a.store, `SELECT <string> id AS id, name, category, <float> price AS price FROM product ORDER BY name;`, nil)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	customers, err := one[[]customer](ctx, a.store, `SELECT <string> id AS id, name, city, <float> fn::customer_total(id) AS total FROM customer ORDER BY name;`, nil)
	writeJSON(w, map[string]any{"products": products, "customers": customers}, err)
}

// ---- graph ----

type edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Qty  int    `json:"qty"`
}

func (a *api) graph(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	edges, err := one[[]edge](ctx, a.store, `SELECT <string> in AS from, <string> out AS to, math::sum(qty) AS qty FROM ordered GROUP BY from, to;`, nil)
	writeJSON(w, edges, err)
}

func (a *api) recommend(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type rec struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	// Customers who bought this product (<-ordered<-customer), then what else they bought (->ordered->product).
	recs, err := one[[]rec](ctx, a.store, `LET $p = <record> $id;
SELECT out.name AS name, count() AS count FROM ordered WHERE in IN $p<-ordered<-customer AND out != $p GROUP BY name ORDER BY count DESC LIMIT 5;`, map[string]any{"id": r.URL.Query().Get("product")})
	writeJSON(w, recs, err)
}

// ---- full-text ----

func (a *api) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type hit struct {
		Product string  `json:"product"`
		Rating  int     `json:"rating"`
		Text    string  `json:"text"`
		Score   float64 `json:"score"`
	}
	// The highlight markers are plain characters; the browser escapes the text and turns them into <mark>.
	hits, err := one[[]hit](ctx, a.store, `SELECT product.name AS product, rating, search::highlight('\u0001', '\u0002', 1) AS text, search::score(1) AS score FROM review WHERE text @1@ $q ORDER BY score DESC LIMIT 10;`, map[string]any{"q": r.URL.Query().Get("q")})
	writeJSON(w, hits, err)
}

// ---- vector ----

func (a *api) similar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type sim struct {
		Name     string  `json:"name"`
		Distance float64 `json:"distance"`
	}
	sims, err := one[[]sim](ctx, a.store, `LET $p = <record> $id;
SELECT name, vector::distance::knn() AS distance FROM product WHERE embedding <|3,40|> $p.embedding AND id != $p ORDER BY distance;`, map[string]any{"id": r.URL.Query().Get("product")})
	writeJSON(w, sims, err)
}

// ---- geospatial ----

func (a *api) stores(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type place struct {
		Name string    `json:"name"`
		Lon  float64   `json:"lon"`
		Lat  float64   `json:"lat"`
	}
	stores, err := one[[]place](ctx, a.store, `SELECT name, <float> location.coordinates[0] AS lon, <float> location.coordinates[1] AS lat FROM store ORDER BY name;`, nil)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	type nearest struct {
		Customer string  `json:"customer"`
		City     string  `json:"city"`
		Lon      float64 `json:"lon"`
		Lat      float64 `json:"lat"`
		Store    string  `json:"store"`
		Km       float64 `json:"km"`
	}
	near, err := one[[]nearest](ctx, a.store, `SELECT name AS customer, city, <float> location.coordinates[0] AS lon, <float> location.coordinates[1] AS lat,
  (SELECT name, geo::distance(location, $parent.location) AS metres FROM store ORDER BY metres LIMIT 1)[0].name AS store,
  <float> math::round(math::min((SELECT VALUE geo::distance(location, $parent.location) FROM store)) / 1000) AS km
FROM customer ORDER BY customer;`, nil)
	writeJSON(w, map[string]any{"stores": stores, "customers": near}, err)
}

// ---- time series + live ----

func (a *api) orders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type bucket struct {
		Minute string `json:"minute"`
		Orders int    `json:"orders"`
	}
	perMinute, err := one[[]bucket](ctx, a.store, `SELECT <string> time::group(at, 'minute') AS minute, count() AS orders FROM order_event WHERE at > time::now() - 30m GROUP BY minute ORDER BY minute;`, nil)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	// ULID keys sort by creation time, so the newest events are a simple descending range.
	latest, err := one[[]orderEvent](ctx, a.store, `SELECT <string> id AS id, <string> at AS at, customer.name AS customer, product.name AS product, qty, <float> amount AS amount FROM order_event ORDER BY id DESC LIMIT 10;`, nil)
	writeJSON(w, map[string]any{"perMinute": perMinute, "latest": latest}, err)
}

func (a *api) live(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	id, ch := a.store.subscribe()
	defer a.store.unsubscribe(id)
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (a *api) traffic(w http.ResponseWriter, r *http.Request) {
	a.store.setTraffic(r.URL.Query().Get("on") == "true")
	writeJSON(w, map[string]bool{"traffic": a.store.trafficRunning()}, nil)
}

// ---- incident ----

// incident damages one thing per data model, so every health check turns red.
func (a *api) incident(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	a.store.setTraffic(false)
	err := a.store.exec(ctx, `UPDATE product SET price = price * 10;
DELETE customer:bob;
DELETE ordered WHERE in = customer:bob;
REMOVE TABLE IF EXISTS review;
REMOVE INDEX IF EXISTS product_vec ON product;
DELETE store:manchester;
DELETE order_event WHERE seed = true;`, nil)
	writeJSON(w, map[string]string{"result": "incident applied"}, err)
}

// ---- health ----

type check struct {
	Model    string `json:"model"`
	Check    string `json:"check"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	OK       bool   `json:"ok"`
}

// health compares the seeded data with what is there now. Traffic only adds unseeded
// records, so it never turns a check red; the incident and a bad restore do.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	type snapshot struct {
		Products   int      `json:"products"`
		PriceSum   float64  `json:"price_sum"`
		HasIndex   bool     `json:"has_index"`
		Customers  int      `json:"customers"`
		SeedEdges  int      `json:"seed_edges"`
		AliceTotal float64  `json:"alice_total"`
		Hits       []string `json:"hits"`
		Similar    []string `json:"similar"`
		Nearest    string   `json:"nearest"`
		SeedEvents int      `json:"seed_events"`
	}
	// Each probe is guarded so that one missing table or index does not hide the others.
	snap, err := one[snapshot](ctx, a.store, `RETURN {
  products: count(SELECT * FROM product),
  price_sum: <float> math::sum(SELECT VALUE price FROM product),
  has_index: (INFO FOR TABLE product).indexes.product_name != NONE,
  customers: count(SELECT * FROM customer),
  seed_edges: count(SELECT * FROM ordered WHERE seed = true),
  alice_total: <float> fn::seed_total(customer:alice),
  hits: (IF (INFO FOR DB).tables.review != NONE THEN (SELECT VALUE <string> id FROM review WHERE text @1@ 'quiet' ORDER BY id) ELSE [] END),
  similar: (IF (INFO FOR TABLE product).indexes.product_vec != NONE THEN (SELECT VALUE name FROM product WHERE embedding <|2,40|> product:kettle.embedding AND id != product:kettle) ELSE [] END),
  nearest: (SELECT name, geo::distance(location, (-1.4701, 53.3811)) AS metres FROM store ORDER BY metres LIMIT 1)[0].name ?? 'none',
  seed_events: count(SELECT * FROM order_event WHERE seed = true)
};`, nil)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	slices.Sort(snap.Similar)
	checks := []check{
		{"Relational", "10 products, prices as seeded, unique name index", "10, 660, index", fmt.Sprintf("%d, %g, %s", snap.Products, snap.PriceSum, map[bool]string{true: "index", false: "no index"}[snap.HasIndex]), snap.Products == 10 && snap.PriceSum == 660 && snap.HasIndex},
		{"Document", "6 schemaless customers", "6", fmt.Sprint(snap.Customers), snap.Customers == 6},
		{"Graph", "14 seeded order edges; fn::seed_total(alice)", "14, 90", fmt.Sprintf("%d, %g", snap.SeedEdges, snap.AliceTotal), snap.SeedEdges == 14 && snap.AliceTotal == 90},
		{"Full-text", "Search 'quiet' finds 2 reviews", "review:r1, review:r6", fmt.Sprint(snap.Hits), slices.Equal(snap.Hits, []string{"review:r1", "review:r6"})},
		{"Vector", "Nearest to Kettle (HNSW)", "[Sandwich press Toaster]", fmt.Sprint(snap.Similar), slices.Equal(snap.Similar, []string{"Sandwich press", "Toaster"})},
		{"Geospatial", "Nearest store to Sheffield", "Manchester", snap.Nearest, snap.Nearest == "Manchester"},
		{"Time series", "20 seeded order events", "20", fmt.Sprint(snap.SeedEvents), snap.SeedEvents == 20},
	}
	writeJSON(w, checks, nil)
}
