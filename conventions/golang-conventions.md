# Go Coding Conventions

Language-level house style for Go services. Persistence rules live in
`database-conventions.md`; test rules in `testing-conventions.md`.

## Code Simplicity

- Prefer flat control flow over deep nesting. Use early returns.
- One function does one thing. If a function exceeds ~40 lines, split it.
- Name variables and functions for what they represent, not for their type.
- Avoid clever one-liners. A few more lines of obvious code beats a compact but opaque expression.
- Small, focused packages with one responsibility per file (~200 lines max).

## Error Handling

- Never swallow errors. Every error must be returned, logged, or explicitly handled.
- Wrap errors with context: `fmt.Errorf("get user by id %d: %w", id, err)`.
- Use `errors.Is` / `errors.As` for sentinel checks, never string comparison.
- Define sentinel errors (`var ErrNotFound = errors.New("not found")`) at package level for conditions callers branch on.
- In HTTP handlers, return early with the appropriate status code and a JSON error body.

## Handler Thinness

HTTP handlers should:

1. Parse and validate input (query params, body).
2. Call a service function or model method.
3. Return the result as JSON.

Business logic belongs in services or model methods, **not** in handlers. This keeps logic testable without HTTP. Register routes through one exported `Routes()` function per domain package; handlers parse input, call the service, return JSON — no other public API from route packages.

## Factorization and Reuse

- Extract shared query/logic patterns into helper functions; don't copy-paste.
- If two handlers do similar work, factor it into a single function with parameters.
- If two routes share logic, share the handler — do not duplicate.
- Reuse constants: define magic values (type IDs, status names, header keys) as package-level constants.

## Parallelization

Use goroutines + `errgroup` (`golang.org/x/sync/errgroup`) when:

- Multiple independent DB queries or external API calls can run concurrently.
- Batch processing where items are independent.

Do **not** parallelize when:

- Operations must be sequential (create parent before child).
- Coordination overhead exceeds the benefit (e.g. two fast queries).
- Shared mutable state would require complex locking.

```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return fetchA(ctx) })
g.Go(func() error { return fetchB(ctx) })
if err := g.Wait(); err != nil {
    return err
}
```

## Context

- Always propagate `context.Context` as the first parameter of any function that does I/O.
- Honor cancellation: pass `ctx` to DB drivers and HTTP clients; check `ctx.Err()` in long loops.
- Never store a `context.Context` in a struct.

## Logging

- Use a structured logger (zap `*zap.SugaredLogger` recommended) — never `fmt.Println` in service code.
- `Info` for significant state transitions, `Error` for failures, `Debug` for development detail.
- Include context in messages: IDs, counts, durations.
- Never log sensitive data (passwords, tokens, full credential-bearing request bodies).

## Code Style

- `gofmt` + `goimports` on save.
- Prefer unexported identifiers; export only what is used outside the package.
- Named return values only when they significantly aid readability.
- Accept interfaces, return concrete types.
- `go vet ./...` and `go build ./...` must pass before committing.
