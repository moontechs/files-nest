# Continuous Watching — manual verification

Prereqs: signed in (server URL + credentials set), library backed up to a steady
state (Pending 0, `.watching`).

- [x] **Add one photo.** Within ~2–3s the panel shows Counting → then a short sync,
      Backed-up climbs by 1, returns to Watching, Pending 0.
- [x] **Import a burst** (e.g. AirDrop 20 photos). A *single* coalesced count+sync
      runs after the burst settles (not 20 separate syncs).
- [x] **Add a photo mid-sync** (add during a large sync). After the current sync
      finishes, a follow-up count+sync picks up the new photo.
- [x] **Pause, then add a photo.** Nothing syncs while paused. On Resume, the held
      change is counted and synced.
- [x] **Relaunch with a backlog** (delete a server item or add photos while the app
      is closed, then launch). The app counts and auto-syncs the backlog on launch.
- [x] **No feedback loop.** After a sync completes it stays at Watching — it does not
      re-trigger itself from its own upload activity.
