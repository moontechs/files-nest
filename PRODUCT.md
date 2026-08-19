# Product

<!-- impeccable:product-schema 1 -->

## Platform

macOS

## Users

Individuals who operate their own FilesNest server and want a dependable way to back up the photos and videos in their Mac Photos library.

## Product Purpose

FilesNest continuously backs up a Mac Photos library to the user's self-hosted server. It makes the state of that backup visible and controllable while preserving data integrity through interrupted or long-running uploads.

## Positioning

Unlike a hosted photo-backup service, FilesNest streams Photos-library resources directly from iCloud to a server the individual operates. The client keeps no local database; the server is the source of truth for sync state.

## Operating Context

The macOS menu-bar app observes the Photos library, assesses pending work, and uploads photos and videos to a configured FilesNest server. Users configure the server URL and HTTP Basic Auth credentials, can test the connection, pause or resume automatic work, manually sync, and inspect failed items. Large or iCloud-only assets are streamed sequentially and uploads can resume after interruption.

## Capabilities and Constraints

- Supports automatic synchronization, manual synchronization, pause/resume, progress, backup counts, and failed-item review.
- Uses PhotoKit to access Photos resources and TUS resumable uploads to the server.
- Streams data without app-owned temporary files or whole-file application buffering; one resource is processed at a time.
- Stores credentials in the macOS Keychain, never in UserDefaults or plists.
- The server persists upload metadata and organizes completed files; it is the sync-state authority.
- The macOS app targets macOS 14. The shared Swift package also supports iOS 17, but the shipped product scope here is the macOS app.
- Additional brand and accessibility commitments are undecided.

## Brand Commitments

FilesNest is a privacy-respecting, self-hosted product. It must not imply a hosted account, cloud storage provider, or guarantees not supported by the user's own server setup.

## Evidence on Hand

- macOS implementation: `apple/macos/FilesNest/FilesNest/`.
- Shared client and synchronization core: `apple/FilesNestCore/`.
- Server behavior and system architecture: `server/README.md` and `docs/architecture.md`.
- Manual verification scenarios: `docs/distribution/pre-tester-verification.md`.
- No externally validated testimonials, customer claims, benchmarks, or marketing assets were found; future work must not fabricate them.

## Product Principles

1. Keep the individual in control of where their photos and videos are backed up.
2. Prefer dependable, resumable transfer and recoverable state over apparent speed.
3. Make backup state and failures legible without turning routine operation into a chore.
4. Respect the Photos library, disk capacity, and credentials as sensitive user resources.
