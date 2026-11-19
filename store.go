package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	surrealdb "github.com/surrealdb/surrealdb.go/v2"
	"github.com/surrealdb/surrealdb.go/v2/pkg/models"
)

// store owns the SurrealDB connection. A Kasten restore restarts SurrealDB, so the
// connection is re-established whenever it drops, and the live query re-registered.
type store struct {
	cfg config

	mu   sync.RWMutex
	db   *surrealdb.DB
	err  error // last connection error, shown in the UI
	info string

	traffic   sync.Mutex
	trafficOn bool
	stopTraf  context.CancelFunc

	subs   sync.Mutex
	nextID int
	feeds  map[int]chan orderEvent
}

type orderEvent struct {
	ID       string  `json:"id"`
	At       string  `json:"at"`
	Customer string  `json:"customer"`
	Product  string  `json:"product"`
	Qty      int     `json:"qty"`
	Amount   float64 `json:"amount"`
}

func newStore(cfg config) *store {
	return &store{cfg: cfg, feeds: map[int]chan orderEvent{}}
}

func (s *store) conn() (*surrealdb.DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		if s.err != nil {
			return nil, s.err
		}
		return nil, errors.New("not connected to SurrealDB yet")
	}
	return s.db, nil
}

func (s *store) status() (connected bool, detail string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return true, s.info
	}
	if s.err != nil {
		return false, s.err.Error()
	}
	return false, "connecting"
}

// run connects, seeds an empty database, and watches order_event with a live query.
// When anything fails it waits and starts again, which is what happens during a restore.
func (s *store) run(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.session(ctx)
		s.mu.Lock()
		if s.db != nil {
			_ = s.db.Close(context.Background())
		}
		s.db, s.err = nil, err
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		log.Printf("SurrealDB session ended: %v; reconnecting in 3s", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *store) session(ctx context.Context) error {
	db, err := surrealdb.FromEndpointURLString(ctx, s.cfg.URL)
	if err != nil {
		return fmt.Errorf("connect %s: %w", s.cfg.URL, err)
	}
	auth := &surrealdb.Auth{Namespace: s.cfg.Namespace, Database: s.cfg.Database, Username: s.cfg.User, Password: s.cfg.Pass}
	if _, err := db.SignIn(ctx, auth); err != nil {
		_ = db.Close(ctx)
		return fmt.Errorf("sign in as %s: %w", s.cfg.User, err)
	}
	if err := db.Use(ctx, s.cfg.Namespace, s.cfg.Database); err != nil {
		_ = db.Close(ctx)
		return fmt.Errorf("use %s/%s: %w", s.cfg.Namespace, s.cfg.Database, err)
	}
	version := "unknown"
	if v, err := db.Version(ctx); err == nil {
		version = v.Version
	}

	s.mu.Lock()
	s.db, s.err = db, nil
	s.info = fmt.Sprintf("SurrealDB %s, %s/%s", version, s.cfg.Namespace, s.cfg.Database)
	s.mu.Unlock()
	log.Printf("connected: %s", s.info)

	if err := s.seedIfEmpty(ctx); err != nil {
		return err
	}

	liveID, err := surrealdb.Live(ctx, db, models.Table("order_event"), false)
	if err != nil {
		return fmt.Errorf("live query on order_event: %w", err)
	}
	notes, err := db.LiveNotifications(liveID.String())
	if err != nil {
		return fmt.Errorf("live notifications: %w", err)
	}

	health := time.NewTicker(5 * time.Second)
	defer health.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case n, ok := <-notes:
			if !ok {
				return errors.New("live query channel closed")
			}
			if n.Action != "CREATE" {
				continue
			}
			if id, ok := recordIDOf(n.Result); ok {
				s.publish(ctx, id)
			}
		case <-health.C:
			if err := db.Health(ctx); err != nil {
				return fmt.Errorf("health check: %w", err)
			}
		}
	}
}

func recordIDOf(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	switch id := m["id"].(type) {
	case models.RecordID:
		return fmt.Sprintf("%s:%v", id.Table, id.ID), true
	case *models.RecordID:
		return fmt.Sprintf("%s:%v", id.Table, id.ID), true
	}
	return "", false
}

// one runs a query and returns the result of its LAST statement.
func one[T any](ctx context.Context, s *store, sql string, vars map[string]any) (T, error) {
	var zero T
	db, err := s.conn()
	if err != nil {
		return zero, err
	}
	res, err := surrealdb.Query[T](ctx, db, sql, vars)
	if err != nil {
		return zero, err
	}
	if res == nil || len(*res) == 0 {
		return zero, errors.New("empty result")
	}
	last := (*res)[len(*res)-1]
	if last.Error != nil {
		return zero, errors.New(last.Error.Message)
	}
	return last.Result, nil
}

// exec runs one or more statements and fails on the first statement error.
func (s *store) exec(ctx context.Context, sql string, vars map[string]any) error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	res, err := surrealdb.Query[any](ctx, db, sql, vars)
	if err != nil {
		return err
	}
	for i, r := range *res {
		if r.Error != nil {
			return fmt.Errorf("statement %d: %s", i+1, r.Error.Message)
		}
	}
	return nil
}

// seedIfEmpty applies the schema on every start (every statement is IF NOT EXISTS or
// OVERWRITE) and loads the data only into an empty database.
func (s *store) seedIfEmpty(ctx context.Context) error {
	if err := s.applyFile(ctx, "seed/schema.surql"); err != nil {
		return err
	}
	n, err := one[int](ctx, s, "RETURN count(SELECT * FROM product);", nil)
	if err == nil && n > 0 {
		return nil
	}
	return s.seed(ctx)
}

func (s *store) applyFile(ctx context.Context, name string) error {
	b, err := seedFiles.ReadFile(name)
	if err != nil {
		return err
	}
	if err := s.exec(ctx, string(b), nil); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (s *store) seed(ctx context.Context) error {
	if err := s.applyFile(ctx, "seed/data.surql"); err != nil {
		return err
	}
	// 20 seeded order events spread over the last 20 minutes, so the chart is not empty.
	if err := s.exec(ctx, `FOR $i IN 1..=20 { CREATE order_event:ulid() SET at = time::now() - <duration> string::concat($i, 'm'), customer = customer:alice, product = product:kettle, qty = 1, amount = 25, seed = true; };`, nil); err != nil {
		return fmt.Errorf("seed order events: %w", err)
	}
	log.Print("seeded the demo dataset")
	return nil
}

// ---- live feed fan-out ----

func (s *store) subscribe() (int, chan orderEvent) {
	s.subs.Lock()
	defer s.subs.Unlock()
	s.nextID++
	ch := make(chan orderEvent, 32)
	s.feeds[s.nextID] = ch
	return s.nextID, ch
}

func (s *store) unsubscribe(id int) {
	s.subs.Lock()
	defer s.subs.Unlock()
	delete(s.feeds, id)
}

func (s *store) publish(ctx context.Context, id string) {
	ev, err := one[[]orderEvent](ctx, s, `SELECT <string> id AS id, <string> at AS at, customer.name AS customer, product.name AS product, qty, <float> amount AS amount FROM <record> $id;`, map[string]any{"id": id})
	if err != nil || len(ev) == 0 {
		return
	}
	s.subs.Lock()
	defer s.subs.Unlock()
	for _, ch := range s.feeds {
		select {
		case ch <- ev[0]:
		default: // a slow browser misses an event rather than blocking the others
		}
	}
}

// ---- traffic generator ----

var (
	trafficCustomers = []string{"alice", "bob", "chen", "dana", "emil", "fatima"}
	trafficProducts  = []string{"kettle", "teapot", "toaster", "blender", "mixer", "juicer", "grinder", "espresso", "frenchpress", "sandwich"}
)

func (s *store) setTraffic(on bool) {
	s.traffic.Lock()
	defer s.traffic.Unlock()
	if on == s.trafficOn {
		return
	}
	s.trafficOn = on
	if !on {
		s.stopTraf()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stopTraf = cancel
	go func() {
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c := trafficCustomers[rand.IntN(len(trafficCustomers))]
				p := trafficProducts[rand.IntN(len(trafficProducts))]
				q := 1 + rand.IntN(3)
				// One transaction: the order event and the graph edge land together or not at all.
				err := s.exec(ctx, `BEGIN;
LET $p = type::record('product', $p);
LET $c = type::record('customer', $c);
CREATE order_event:ulid() SET at = time::now(), customer = $c, product = $p, qty = $q, amount = $p.price * $q;
RELATE $c->ordered->$p SET qty = $q, at = time::now();
COMMIT;`, map[string]any{"c": c, "p": p, "q": q})
				if err != nil && !strings.Contains(err.Error(), "not connected") {
					log.Printf("traffic: %v", err)
				}
			}
		}
	}()
}

func (s *store) trafficRunning() bool {
	s.traffic.Lock()
	defer s.traffic.Unlock()
	return s.trafficOn
}
