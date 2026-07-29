# Manual verification: Real photo thumbnails

Checklist for the sync-strip + failed-list thumbnails (design `docs/design/20260729-sync-thumbnails.md`). Run the built app.

## Sync strip
- [ ] During a sync (`.syncing` with a current item), the square next to the filename shows the **actual photo**, not the gradient.
- [ ] As the current item changes, the thumbnail updates to match each file.
- [ ] Briefly at the start of each item, the gradient may show before the image loads (acceptable), then resolves to the photo.

## Failed list
- [ ] Open the failed list (tap the Failed tile when > 0). Each row shows the **photo thumbnail** to the left of filename + reason.
- [ ] Scrolling the list stays smooth (no stutter from image loads).

## Fallbacks
- [ ] A failed *delete* row (asset no longer in the library) or any missing asset shows the **gradient placeholder**, not a broken image.
- [ ] Signed out / no current item → no crash; sync strip isn't shown at rest anyway.

## Notes
- Thumbnails are cached in-memory (`NSCache`); re-opening the failed list should render instantly the second time.
- `.opportunistic` delivery may show a low-res image first; confirm it sharpens.
