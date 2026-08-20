# Keep upload finalize/move synchronous, not backgrounded

`PATCH /uploads/{id}/status` still blocks until `filestore.Mover` finishes moving the completed file into the organized tree before responding, even though this work adds support for async/multithreaded uploads.

We considered returning immediately and performing the move in a background goroutine, with the client learning the outcome via polling `GET /uploads/{id}`. We rejected it: the move is a same-filesystem rename (fast, already serialized behind one mutex), and backgrounding it would trade a simple definitive success/fail response for a polling contract plus a new failure-visibility problem — a background move failing after the client was already told "done" has no clean way to surface. "Async uploads" in this project means multiple uploads proceeding concurrently, not that any individual upload's completion step is non-blocking.
