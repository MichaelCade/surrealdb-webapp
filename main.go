// Command surrealdb-webapp is a demo shop that shows SurrealDB's data models side by
// side (document, relational, graph, full-text, vector, geospatial, time series) and
// a health panel that turns red when data is damaged and green again after a restore.
//
// It is built to demonstrate protecting SurrealDB with Kasten, not as a template for
// a production application.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed static
var staticFiles embed.FS

//go:embed seed/schema.surql seed/data.surql
var seedFiles embed.FS

type config struct {
	URL, Namespace, Database, User, Pass, Addr string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := config{
		URL:       env("SURREAL_URL", "ws://surrealdb:8000"),
		Namespace: env("SURREAL_NS", "demo"),
		Database:  env("SURREAL_DB", "shop"),
		User:      env("SURREAL_USER", "demo"),
		Pass:      os.Getenv("SURREAL_PASS"),
		Addr:      env("LISTEN_ADDR", ":8080"),
	}
	if cfg.Pass == "" {
		log.Fatal("SURREAL_PASS is not set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := newStore(cfg)
	go store.run(ctx) // connects, seeds when empty, keeps the live query running

	mux := http.NewServeMux()
	api := &api{store: store}
	api.routes(mux)
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(static)))

	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("listening on %s, SurrealDB %s (%s/%s)", cfg.Addr, cfg.URL, cfg.Namespace, cfg.Database)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
