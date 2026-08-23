# Adopt go-pkgz/lgr via stdlib log redirect, no JSON output

Server logging was plain stdlib `log` with no levels, no structured output, and ~83 call sites scattered mostly across `internal/api/handlers.go` and `recovery.go`. We migrated to `github.com/go-pkgz/lgr` for leveled logging (`LOG_LEVEL=info|debug|trace` env var, default `info`), with INFO reserved for lifecycle events and warnings/errors, and all happy-path/per-request logging moved to DEBUG.

We wired it in via `lgr.SetupStdLogger(opts...)`, which redirects existing `log.Printf`/`log.Println` calls through `lgr`'s formatting and level filtering, rather than rewriting all call sites to hold and call an explicit `lgr.Logger` instance. The redirect approach needed no new dependency threaded through structs/handlers (none currently accept one); the only per-call-site change was prefixing DEBUG-level messages with `"DEBUG "`.

We did not enable `lgr`'s JSON output (`lgr.SlogHandler(slog.NewJSONHandler(...))`): there is no log aggregation pipeline today, logs are consumed via `docker logs`/CI, and plain text is more readable there. JSON can be added later behind a `LOG_FORMAT` env var if a log aggregator is introduced.

This migration also changes which stream most log output goes to: stdlib `log` defaults to `os.Stderr` (every current call site writes there), while `lgr.SetupStdLogger`'s output path writes INFO/DEBUG/WARN lines to stdout only, adding stderr as a second destination just for ERROR/FATAL/PANIC. We accept this because `docker compose logs` (the only log consumer identified) merges both streams by default — but it means "logs go to stdout" becomes true *as a result of* this migration, not a pre-existing fact that justified skipping JSON.

## Considered Options

- Thread an explicit `lgr.Logger` through every struct/handler that logs — rejected as unrequested architectural churn; none of the ~83 call sites need to change behavior individually, only their output formatting/filtering.
- Enable JSON output now — rejected as premature; no consumer expects structured logs today.
