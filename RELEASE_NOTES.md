## Performance

- **Much lower CPU usage while Claude Code sessions are active.** Session
  parsing now goes through a per-file cache validated by mtime, size, and
  pricing version, so a session write re-parses only the file that changed
  instead of the whole corpus. Previously the watcher recomputed the sessions
  list and daily aggregate from scratch on every change event, which added up
  on large session histories. Idle CPU with an active session now sits at
  effectively zero.

- **Faster `/api/today` hourly histogram.** The hourly token buckets are
  computed per file and cached under the same scheme, instead of rescanning
  every session file on each request.

## Fixes

- **Session costs stay in sync with pricing.** Cached aggregates are now
  invalidated when the pricing table refreshes, so USD costs always reflect
  current prices.
