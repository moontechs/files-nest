# Manual verification — client concurrent uploads

Prereqs: a server running PR #25 (default cap 4) with real credentials, and a
library with enough pending items to see several uploads at once. A Limited
Photos Library (~10 selected) is enough.

- [ ] Trigger a sync with several pending items. The strip subtitle shows
      "Uploading N · X of Y" with N > 1 while multiple uploads are in flight.
- [ ] The hero thumbnail changes to the most-recently-started item and keeps
      moving; "Backed up" climbs faster than the pre-change serial build.
- [ ] Pause mid-burst: the sync stops promptly, no crash, "Pending" is honest.
- [ ] Point at a pre-#25 server (no /config): sync still runs; no unhandled
      errors surface (falls back to cap 4).
- [ ] Force a 503 path if possible (set MAX_CONCURRENT_UPLOADS=1 on the server,
      run several uploads): items still complete (client retries), none land in
      the Failed list from transient 503s.
