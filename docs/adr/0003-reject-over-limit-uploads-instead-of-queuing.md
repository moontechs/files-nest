# Reject over-limit uploads with 503 instead of queuing server-side

When concurrent uploads (see CONTEXT.md) exceed `MAX_CONCURRENT_UPLOADS`, the server rejects the new `PATCH /uploads/{id}/data` request immediately with `503` + `Retry-After`, rather than holding the request open until a slot frees.

We considered server-side queuing (block the handler until capacity is available), but rejected it: it accumulates blocked goroutines and open connections under load, adds timeout/queue-depth logic with no clear bound, and duplicates retry behavior the TUS protocol's resumable-upload clients already need to implement anyway. Immediate rejection keeps the server stateless about backpressure and pushes retry/backoff to the client, which is already the natural fit for a resumable protocol.
