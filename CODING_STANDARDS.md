# Review checklist

Use these questions when reviewing asynchronous processing, retries or retained
operational history. Trace the answer across callers, workers and persistence;
test output alone does not establish it.

- **Scope:** Does a quota, pause or cooldown govern an event, a target or the
  deployment? Do queued work, concurrent workers and restart enforce that same
  scope? Deferred work should retain its retry budget; distinguish sends already
  admitted from later attempts.
- **Clock:** Where was each time value captured? Follow it through the outer
  worker, queue and response handler. Check slow batches and buffered wakeups;
  response-relative delays start from the response, using a current clock anchor.
  Preserve explicit virtual clocks where replay requires them.
- **Reachability:** Can an operator inspect every retained outcome, including a
  recent failure after more than one page of history? Check ordering, pagination
  and lookup behavior through the supported CLI/API.

These are review judgments. Existing notification regressions already cover the
concrete M6 failures; keep those checks in the normal verification command.
