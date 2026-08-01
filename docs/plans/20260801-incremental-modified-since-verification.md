# Incremental sync range — manual verification

Prereqs: signed in; a **Limited Photos Library** (~10 selected) backed up to a
steady state (Pending 0, Watching). Watch the `🟢 FN library:` console log for the
range on each enumeration (`enumeration start (range=…)`).

- [ ] **Add a recent photo** to the selected set. The change enumeration logs
      `range=modifiedSince(…)` (not `.all`), counts + syncs it, Backed-up +1.
- [ ] **Add a photo with an OLD capture date** (import/AirDrop something taken long
      ago into the selected set). It is still caught — the window is on
      modificationDate, not creationDate — and backs up.
- [ ] **Edit an existing photo** (crop/adjust). Its modificationDate bumps → the
      incremental cycle re-uploads it.
- [ ] **Delete a backed-up photo** from the selection. The incremental cycle does
      NOT remove the server record (upload-only). Relaunch the app → the launch
      `.all` (`range=all` in the log) reconciles and deletes the server record.
- [ ] **Launch** always logs `range=all`; **Sync Now** always logs `range=all`.
- [ ] Backed-up / Pending tiles stay whole-library correct across incremental
      cycles (they do not collapse to a small window count).
