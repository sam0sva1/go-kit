# go-kit

Small, dependency-free Go helpers extracted from several services that had
grown their own copies of the same code.

Go 1.24+. No external dependencies.

```
go get github.com/sam0sva1/go-kit
```

## retry

Retries an operation that may fail on the first try.

With no options the behaviour matches the common hand-rolled version — N
attempts with a fixed delay — so adopting it does not silently change how
existing call sites behave. Backoff, logging and error filtering are opt-in.

```go
err := retry.Do(ctx, func(ctx context.Context) error {
    return connect(ctx)
}, retry.WithAttempts(5), retry.WithDelay(5*time.Second))
```

Options: `WithAttempts`, `WithDelay`, `WithBackoff`, `WithLogger`,
`WithRetryIf`, `WithSleeper`.

What it fixes relative to the copies it replaces:

- `attempts <= 0` used to return `nil` without ever calling the operation —
  a silent success with no work done. It now runs once.
- There is no pause after the final attempt. The previous version always
  slept before giving up, wasting one full delay on every failure.
- Waiting is interruptible. The previous version used `time.Sleep`, so a
  shutdown signal was ignored for the remainder of the retry schedule.
- The last error is returned wrapped, so `errors.Is` and `errors.As` work.
  When a wait is cut short by cancellation, the cancellation cause is
  wrapped in alongside it.

Guards, because this is a published package: a nil operation returns an error
instead of panicking; a backoff factor that is NaN, infinite or below one is
treated as no growth rather than producing nonsense delays; growth is checked
for Duration overflow; and the ceiling applies to the first delay, not only to
grown ones. A failure of the injected sleeper is reported as a wait failure, not
mislabelled as a cancellation.

Backoff is deliberately opt-in: one of the adopting call sites retries with
a zero delay, and a default backoff would have changed its behaviour without
anyone noticing.

## License

MIT
