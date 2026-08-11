# Pre-tester end-to-end verification

One consolidated manual pass to walk **before** handing the macOS app to testers.
It merges the per-slice checklists that shipped unwalked (#11 count-on-start, #12
thumbnails, #13 continuous watching, #15 incremental range, #16 restart-reconcile)
into a single run against a **real server** and a **real photo library**.

This is the gate the automated tests can't cover: the app-target integration —
PhotoKit → uploads → server, the panel UI, first-run permissions, behavior at
scale. FilesNest is a *backup* app, so treat data integrity as the pass/fail bar.

## Setup

- **Server**: a reachable FilesNest server (dev or a real one) whose `organized/`
  output you can inspect. Note the base URL + credentials.
- **Two library modes** (do both):
  - **Limited Photos Library (~10 selected)** for fast iteration on the flows.
    System Settings → Privacy & Security → Photos → FilesNest → *Selected Photos*.
    Use *Manage Selected Photos…* to add/remove during the run.
  - **The full ~70k library** for one scale/correctness run (§2).
- **Console**: run from Xcode (Run the `FilesNest` scheme) to see the DEBUG traces:
  `🟢 FN library: enumeration start (range=…)`, `🟣 FN engine: status → …`.
- Before each independent check, get to a **steady state**: Pending 0, status
  *Watching*.

Tick every box. Record anything surprising inline.

## 1. First run

- [ ] Fresh launch (no prior sign-in). The menu-bar icon appears; opening the panel
      shows a signed-out / sign-in prompt.
- [ ] Sign in via the web/OAuth flow; the server URL + credentials persist (relaunch
      stays signed in).
- [ ] First library access triggers the **Photos permission** prompt; granting it
      lets enumeration proceed. Denying it surfaces a clear state (not a silent hang).
- [ ] With Limited Library, only the selected photos are counted.

## 2. Full backup at scale (the correctness run — full ~70k library)

- [ ] Grant full Photos access. On launch the panel shows the determinate
      **Counting N of M** hero, then an exact at-rest **Pending**.
- [ ] Launch **auto-syncs** the backlog (option A) without a manual tap; `range=all`
      in the console. Let it run to completion.
- [ ] **Backed up climbs live** during the sync; **Pending** falls to 0.
- [ ] Inspect the server: files land under `organized/<user>/<year>/<month>/<day>/…`
      and the count matches. **No missing or duplicated files.**
- [ ] Relaunch → warm launch seeds instantly, recounts, and settles to Watching with
      Pending 0 (no spurious re-upload of everything).

## 3. Continuous watching (#13)

- [ ] **Add one photo** → within ~2–3s: Counting → short sync → Backed up +1 →
      Watching, Pending 0.
- [ ] **Import a burst** (AirDrop/drag ~20 at once) → a **single** coalesced count+sync
      after the ~2s debounce settles (not 20 separate syncs).
- [ ] **Add a photo mid-sync** → after the current sync finishes, a follow-up
      count+sync picks it up.
- [ ] **Pause, add a photo** → nothing syncs while paused; on **Resume** the held
      change is counted and synced.
- [ ] **No feedback loop** → after any sync completes it stays at Watching (the app's
      own uploads don't re-trigger it).

## 4. Incremental range (#15)

- [ ] **Add a recent photo** → the change enumeration logs `range=modifiedSince(…)`
      (not `.all`), and backs it up.
- [ ] **Import an OLD-dated photo** (something taken long ago) → still caught (the
      window is on modificationDate, not creationDate) and backed up.
- [ ] **Edit an existing photo** (crop/adjust) → its modificationDate bumps → the
      incremental cycle re-uploads it.
- [ ] **Delete a backed-up photo** → the incremental cycle does **not** remove the
      server record; **relaunch** → the launch `.all` (`range=all`) reconciles and
      deletes the server record.
- [ ] **Launch** and **Sync Now** always log `range=all`.
- [ ] Backed-up / Pending tiles stay **whole-library correct** across incremental
      cycles (they don't collapse to a small window count).

## 5. Restart forces reconcile (#16)

- [ ] **Save Settings mid-sync** → the in-flight run is superseded and a fresh
      `range=all` count+sync starts immediately (not deferred).
- [ ] **Change the server URL mid-sync** (point at a second, empty server) and Save →
      the new server receives the **full** backup (`range=all`), not just a window.
- [ ] **Save Settings while paused** → refreshes Pending (`range=all` count) but does
      **not** upload; stays paused-then-watching without a sync.
- [ ] After a reconcile, the next library change first scans `range=all` (anchor
      reset), then returns to `range=modifiedSince(…)` once a clean sync re-grounds it.

## 6. Thumbnails & panel UI (#12)

- [ ] The sync-strip current item shows the **real photo thumbnail** (not the gradient
      placeholder), and it updates as items advance.
- [ ] Missing/deleted assets fall back to the gradient gracefully (no crash).
- [ ] The panel is legible: tiles (Backed up / Pending / Failed), status text, and the
      Pause/Resume/Sync Now controls behave as labeled.
- [ ] The **"X of Y backed up"** caption under the tiles shows during syncing / at rest
      (e.g. "63,201 of 70,444 backed up"), with thousands separators, and updates as
      backed-up climbs. It's **hidden during "Counting…"** (the ring counts assets, the
      caption counts resources — different units) and absent when signed out.

## 7. Failure handling

- [ ] Force a failure (e.g. stop the server mid-sync, or an unwritable path) → the
      sync doesn't crash; failed items are recorded and the **Failed** tile + the
      **Failed items** slide-in list show them with filenames.
- [ ] Recover (server back up) + Sync Now → failures retry and clear.
- [ ] Wrong credentials / unreachable server surfaces an error state, not a hang.

## 8. Lifecycle

- [ ] **Pause** during a sync actually cancels the in-flight work (status → Paused with
      a remaining count); **Resume** starts fresh.
- [ ] **Quit and relaunch** → no corruption; counts reconcile correctly.
- [ ] Sign out / sign in → summary clears on sign-out, re-establishes on sign-in.
- [ ] Leave it running idle for a while → it keeps watching; adding a photo later still
      triggers a backup (the observer is still registered).

## Exit criteria

Ship to testers only when §2 (the scale/correctness run) is clean — server contents
exactly match the library — and no box in §§1,3–8 fails. File any failure as an issue
before distributing.
