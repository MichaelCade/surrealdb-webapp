-- Multi-model demo shop (namespace demo, database shop). One statement per line:
-- the surreal CLI reads piped input one line at a time.
-- Document + relational: a schemafull catalogue with a unique index.
DEFINE TABLE IF NOT EXISTS product SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS name ON product TYPE string;
DEFINE FIELD IF NOT EXISTS category ON product TYPE string;
DEFINE FIELD IF NOT EXISTS price ON product TYPE number;
DEFINE FIELD IF NOT EXISTS description ON product TYPE string;
-- Vector: 6 hand-made features [kitchen, electric, heat, blend, coffee, budget], so similarity is explainable.
DEFINE FIELD IF NOT EXISTS embedding ON product TYPE array<float>;
DEFINE INDEX IF NOT EXISTS product_name ON product FIELDS name UNIQUE;
DEFINE INDEX IF NOT EXISTS product_vec ON product FIELDS embedding HNSW DIMENSION 6 DIST COSINE;
-- Document: schemaless customers, with a geo point for each.
DEFINE TABLE IF NOT EXISTS customer SCHEMALESS;
-- Graph: customer->ordered->product edges.
DEFINE TABLE IF NOT EXISTS ordered TYPE RELATION IN customer OUT product SCHEMALESS;
-- Full-text: product reviews.
DEFINE ANALYZER IF NOT EXISTS review_en TOKENIZERS blank, class FILTERS lowercase, snowball(english);
DEFINE TABLE IF NOT EXISTS review SCHEMALESS;
DEFINE INDEX IF NOT EXISTS review_text ON review FIELDS text FULLTEXT ANALYZER review_en BM25 HIGHLIGHTS;
-- Geospatial: stores.
DEFINE TABLE IF NOT EXISTS store SCHEMALESS;
-- Time series: order events with ULID keys (time-ordered).
DEFINE TABLE IF NOT EXISTS order_event SCHEMALESS;
DEFINE INDEX IF NOT EXISTS order_event_at ON order_event FIELDS at;
-- Stored function over the graph.
DEFINE FUNCTION OVERWRITE fn::customer_total($c: record<customer>) { RETURN math::sum((SELECT VALUE out.price * qty FROM ordered WHERE in = $c)) ?? 0; };
-- Same total over the seeded orders only: what the health check compares, so live traffic never turns it red.
DEFINE FUNCTION OVERWRITE fn::seed_total($c: record<customer>) { RETURN math::sum((SELECT VALUE out.price * qty FROM ordered WHERE in = $c AND seed = true)) ?? 0; };
