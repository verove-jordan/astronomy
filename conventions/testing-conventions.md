# Testing Conventions

## Guiding Principles (all languages)

1. **Consistency over cleverness** — every test follows the same structure, naming, and assertion style.
2. **Protect critical paths** — focus on code where breakage cascades: data integrity, authentication, core logic, public API contracts.
3. **Tests are documentation** — names and structure read as a specification.
4. **Stability over coverage** — a flaky test is worse than no test. Every test is deterministic and independent (no shared mutable state, no execution-order reliance).
5. **Test modifications are breaking changes** — editing or deleting an existing test changes the contract it protected. That requires explicit justification.

General: arrange-act-assert structure; tests colocated with the code they test; clean state at the **start** of each test, not the end; prefer real dependencies (real DB in a container) over mocks where practical.

## Go

- Frameworks: stdlib `testing`, `testify/require` (preconditions) + `testify/assert` (verification), `dockertest` for real Postgres/ClickHouse, `httptest` for external APIs.
- **Table-driven** tests with `t.Run` per case.
- Naming: `TestType_Method_Scenario` or `TestFunc_Scenario` (e.g. `TestUser_Search_CaseInsensitive`).
- Test files next to the code; integration tests `*_integration_test.go`.
- No `sqlmock`/`gomock` — use real DBs. No hardcoded ports (use `freeport`). No build tags for categorization — use naming.

```go
func TestUser_Search(t *testing.T) {
    db := testdb.Get(t); db.AutoMigrate(&User{})
    tests := []struct{ name, query string; seed []User; want int }{
        {"case insensitive", "ACME", []User{{Name: "acme"}, {Name: "other"}}, 1},
        {"no match", "zzz", []User{{Name: "acme"}}, 0},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            db.Exec("DELETE FROM users")
            for _, u := range tt.seed { db.Create(&u) }
            got, err := User{}.Search(db, tt.query)
            require.NoError(t, err); assert.Len(t, got, tt.want)
        })
    }
}
```

## Python

- `pytest`. Test files `test_*.py` (colocated or under `tests/`). Functions `test_*`.
- **`@pytest.mark.parametrize`** for table-driven cases. Fixtures for setup/teardown.
- `pytest.raises(SpecificError)` for error paths. `pytest-asyncio` for async code.
- No real network — use a local container (testcontainers / a Compose service) or an HTTP mock (`respx`/`responses`). No `time.sleep` for synchronization.

## Vue / TypeScript

- `vitest` + `@vue/test-utils` + `@pinia/testing` (+ `happy-dom`). Playwright for E2E.
- File naming `<Name>.spec.ts`, colocated with the unit under test.
- Mock stores via `createTestingPinia({ createSpy: vi.fn })`; mock modules via `vi.mock('@/...')`.
- No real HTTP, no `setTimeout`, no execution-order reliance.

## When to write tests / when not

**Write:** every function with non-trivial logic; every search/filter; every create/update hook; every handler with business logic; auth and permission logic; pipeline/queue changes.

**Skip:** trivial CRUD that just calls the ORM; pure configuration; route/DI wiring.
