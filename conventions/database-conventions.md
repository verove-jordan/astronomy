# Database Conventions

House style for persistence, independent of language or ORM. The opinions here
(ms timestamps, lowercase search, one migration mechanism) are deliberate — keep
them consistent across every service so data is portable between them.

## Every Persisted Entity

Must have:

- A surrogate primary key `id` (auto-increment integer, or UUID if distributed).
- `created_at` and `updated_at` as **int64 millisecond Unix timestamps** — not native `datetime`/`timestamptz`. This keeps timestamps identical across Go, Python, JS, and both OLTP and analytics stores.
- Timestamps set automatically: ORM create/update hooks, DB triggers, or a shared base model. `created_at` set once on insert; `updated_at` on every write.

```go
// Go (GORM) example of the house pattern
type Base struct {
    ID        int64 `gorm:"primaryKey;autoIncrement" json:"id"`
    CreatedAt int64 `gorm:"column:created_at"        json:"created_at"`
    UpdatedAt int64 `gorm:"column:updated_at"        json:"updated_at"`
}
func (b *Base) BeforeCreate(tx *gorm.DB) error {
    now := time.Now().UnixMilli(); b.CreatedAt, b.UpdatedAt = now, now; return nil
}
func (b *Base) BeforeUpdate(tx *gorm.DB) error {
    b.UpdatedAt = time.Now().UnixMilli(); return nil
}
```

## Naming

- Tables `snake_case`, plural (`users`, `creative_files`). Columns `snake_case`.
- Foreign keys named `<entity>_id`. Be explicit with column names; don't rely on ORM defaults.

## Search

- All text search is **case-insensitive**. Match in lowercase: `WHERE LOWER(name) LIKE LOWER(?)`, or store a normalized lowercase column and index it.
- Index every column used in `WHERE`, `ORDER BY`, `JOIN`, or search. Index foreign keys.
- Don't over-index write-heavy tables — each index is write cost.

## Queries

- **Always parameterize.** Never interpolate user input into SQL strings.
- Prefer the ORM / query builder for CRUD. Drop to raw SQL only for analytics or performance, and review it like code.
- Scope queries with `context`/timeouts so a slow query is cancellable.

## Migrations

- Choose **one** authoritative mechanism per project and never mix:
  - Code-driven (ORM `AutoMigrate` / models as source of truth), **or**
  - Versioned SQL migration files (`golang-migrate`, `alembic`, `goose`).
- Migrations are reviewed like code. **Never edit a migration that has shipped** — add a new one.
- Run migrations from a single instance/step, not concurrently from every replica.

## Connections

- Use a pooled connection; size the pool deliberately. Always release/return connections (context managers, `defer`).
- Read DSNs/credentials from env or a secret store — never hardcode, never commit.

## OLTP vs Analytics (when applicable)

- Transactional CRUD → a relational OLTP store (PostgreSQL).
- Heavy aggregation/analytics → a columnar store (ClickHouse, DuckDB), often modeling the same data with an append/replacing engine.
- Keep the same `id` + ms-timestamp conventions in both so rows line up across stores.

## Don'ts

- No business logic in triggers — keep it in the application.
- No `time.Time`/`timestamptz` for `created_at`/`updated_at` (use int64 ms).
- No soft-delete columns unless a requirement genuinely needs them.
- No storing secrets or PII unencrypted.
