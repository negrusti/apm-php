# Slow transaction traces: implementation and improvements

Reviewed 25 October 2026, against upstream release `v12.12.0.41`, commit `cf77822cd783c43cf3f8bb2acc1caa22c49e23cb`.

Project constraints: retain the stock PHP module, run our daemon on the client's machine, serve only a few active clients, and retain data for approximately seven days. Few clients does not establish request volume or PHP worker concurrency.

## Main conclusion

Reuse the module's existing trace capture. The largest immediate improvements are in the daemon's selection and delivery policies: preserve endpoint diversity, use count and byte budgets, retry selected traces, and avoid unnecessary serialization in our custom outbound protocol.

The default daemon retains only one ordinary trace per application/run per harvest, normally 60 seconds. All PHP workers sharing that application identity compete for that slot. Increasing a global limit alone does not ensure diversity: one slow route can still occupy every slot.

The PHP module has already timed functions, selected trace segments, serialized JSON and sent the trace before the daemon decides whether to retain it. Daemon changes can reduce retained memory, export CPU, network traffic and backend work. They cannot eliminate that earlier PHP instrumentation/serialization cost or recover discarded details.

## Implementation, end to end

Source references below are relative to `upstream/newrelic-php-agent/` and include one-based line numbers.

### 1. Instrumentation and segment collection

`agent/php_execute.c:1207` evaluates completed PHP function segments. Functions with metrics, sufficient duration, identifiers, attributes or errors can be retained. The `expensive_min` setting defaults to 2 ms in `agent/php_nrini.c:1103`, so the final call tree should not be interpreted as an exhaustive PHP profiler output. Framework, datastore and external instrumentation add their own segments.

`newrelic.transaction_tracer.detail=1` is the documented default and times user functions. A value of 0 limits detail to selected instrumentation and configured custom tracers. Internal-function tracing is a separate setting, disabled by default.

There are two different segment limits:

- **Capture-time limit:** `max_segments_web` defaults to 0 (unlimited retained segment count), while `max_segments_cli` defaults to 100000. These govern per-request/process segment retention while executing. They do not impose a fixed process memory ceiling.
- **Final trace limit:** `axiom/nr_txn.c:1464` passes `NR_MAX_SEGMENTS` into finalization; `axiom/nr_limits.h:38` defines it as 2000. Under truncation the trace heap selects by inclusive segment duration, not by a complete call-chain or N+1 diagnostic policy.

Setting a capture-time limit protects against pathological requests but has a measurable correctness tradeoff: `axiom/nr_segment.c:79` records zero exclusive time for metrics of discarded segments when the limit is enabled, instead of calculating their full exclusive time. Do not describe reduced segment capture as having no effect on timing detail.

### 2. Slow-transaction qualification

`axiom/nr_txn.c:2319`, `nr_txn_should_save_trace`, checks for existing segments and compares transaction duration against the trace threshold. Synthetics transactions have an explicit exception to the duration test.

The default INI threshold is `apdex_f`, meaning four times Apdex T. `nr_txn_begin` reads `apdex_t` from connect JSON, falling back to 0.5 seconds; that yields a typical 2-second slow-trace threshold (`axiom/nr_txn.c:493`). This default can miss a 600 ms request that is already problematic for a fast endpoint.

The source supports per-transaction Apdex values through `web_transactions_apdex` in the connect reply (`axiom/nr_txn.c:795`). These change trace thresholds only when `apdex_f` remains active. Setting a fixed INI threshold overrides that relationship. This gives our backend a stock-module-compatible way to set different thresholds for known transaction names without modifying PHP code.

The connect reply must also enable the intended collection features. In particular, missing `collect_traces` defaults to false when transaction options are populated (`axiom/nr_txn.c:318`).

### 3. Final trace construction

`axiom/nr_segment_tree.c:9` finalizes the segment tree:

1. Traverse it to calculate total time/metrics and populate bounded trace/span heaps where necessary.
2. If trace or span output is required, traverse again and serialize the selected data.

`axiom/nr_segment_traces.c:483` builds a JSON trace payload containing a nested tree, transaction attributes/intrinsics, and a string dictionary. Segment names refer to dictionary indexes. Datastore/external segments can carry SQL, backtraces, database identity and external-call details.

When some segments are omitted, retained descendants are attached to the nearest retained ancestor (`nr_segment_traces.c:260`). The resulting tree can therefore omit intermediate functions. It is a reconstructed diagnostic tree, not necessarily the original complete call hierarchy.

Segment start/stop offsets are converted from microseconds to **integer milliseconds** at `nr_segment_traces.c:287`. The backend cannot recover sub-millisecond precision from this tree. The outer trace timestamp and duration use doubles, so those fields have different precision from the individual nodes.

This is a slow transaction trace distinct from the sampled distributed span stream: the finalizer uses separate qualification and sampling paths for traces and spans. A slow trace must not be assumed to imply a complete cross-service distributed trace.

### 4. Local delivery and daemon selection

`axiom/cmd_txndata_transmit.c:528` places trace JSON, start timestamp, duration, transaction GUID and a force-persist flag inside the transaction FlatBuffer. A local transaction message also contains other telemetry.

`daemon/internal/newrelic/commands.go:178` reads that trace and checks `IsKeeper` before copying its large JSON byte slice. This avoids retaining a payload copy for rejected traces; preserve that useful optimization in a fork.

`daemon/internal/newrelic/txn_traces.go:113` creates three separate duration-based heaps:

| Category | Default retained count per harvest |
| --- | ---: |
| Ordinary | 1 |
| Force persisted | 10 |
| Synthetics | 20 |

Limits are constants in `limits/limits.go:43`. Synthetics takes precedence over force persisted. Force-persist does not automatically mean an error: `axiom/nr_txn.c:1673` derives it from inbound/outbound record-TT state.

Default data, including transaction traces, is normally harvested every 60 seconds. Faster event harvest settings do not automatically shorten the trace interval: `harvest_trigger.go:111` builds a separate default-data trigger.

### 5. Export and failure handling

`txn_traces.go:28` zlib-compresses and base64-encodes each retained JSON tree. The collector payload is `[run_id, traces]`, where each trace includes timestamp, duration, transaction name, URI, encoded tree, GUID and flags. `collector/client.go:348` then zlib-compresses the entire outbound JSON request again.

This nested encoding supports the New Relic collector format. Our own backend protocol can avoid the per-trace base64 wrapper and use one compressed batch. Any performance gain must be measured; nested compression is redundant work, but removing it is not automatically a universal wire-size improvement.

`TxnTraces.FailedHarvest` at `txn_traces.go:165` is empty. The processor may call it for retryable HTTP responses, but it restores no traces. Network failures also do not have a trace-specific recovery path. Selected traces are lost after failed export.

Two size constraints matter:

- `listener.go:32` caps the entire local transaction message at 2 MiB. An oversized message can lose the other telemetry bundled with that transaction as well.
- The collector client checks the compressed batch against `MaxPayloadSize` (`collector/client.go:354`), defaulting to 1,000,000 bytes unless overridden at connect. Transaction traces have no size-based batch splitting path; the separate split function in `processor.go:654` handles transaction events.

Increasing trace counts without a byte budget and size-aware batching can therefore cause an entire trace batch to be rejected locally or by our backend.

## Recommended changes, in priority order

### 1. Keep representative slow traces across routes

Replace a single application-wide winner with a bounded selection policy keyed by normalized transaction name. Use the module's transaction name where stable, not raw URLs containing IDs or query strings.

For a few clients, admit all qualifying ordinary traces while below the configured budget. When the budget is reached, reserve diversity across active routes and choose the slowest examples within each route. Bound the number of route buckets too, so arbitrary transaction names cannot grow memory indefinitely.

An initial experiment could use two exemplars per route, up to 30 ordinary traces and 8 MiB of retained uncompressed trace bodies per application per minute, plus a daemon-wide ceiling. These are proposed benchmark settings, not validated capacity or performance claims. Reserve explicit, bounded capacity for error-bearing candidates; do not allow forced/synthetic categories to bypass the daemon-wide byte cap.

Simply raising `MaxRegularTraces` to 30 is less useful: repeated 5-second report requests can still crowd out a 900 ms checkout problem. Preserve accurate counters for candidates, admissions, route diversity, rejected bytes and each drop reason.

### 2. Preserve selected traces through transient failures

Add bounded retries with backoff and jitter. If restart survival is needed, use a small append-only file spool instead of adding a separate queue service for this client count.

Specify the acknowledgement boundary, queue count/byte/age limits, eviction policy and backend deduplication key. Use tenant/application identity plus transaction GUID and start timestamp, rather than a transient run ID alone, to recognize retransmissions across reconnects. A local durable write or successful backend acknowledgement must precede reporting durable acceptance.

Size the queue for a short expected outage; seven-day backend retention is not a reason to keep seven days of queued client data. Reject/expire queued records that are already outside the backend retention window. This changes delivery behavior only after data reaches the daemon; it cannot fix losses in the module's local socket submission.

### 3. Budget bytes and split exports by size

Make trace selection count-aware and byte-aware. Cap individual trace size, total retained trace bytes, in-flight export bytes and spool size. Split custom backend batches before submission; isolate an individually oversized record instead of dropping unrelated traces with it.

Keep a compact metadata record and an explicit reason when a tree is rejected, if it fits the diagnostic policy. Avoid silently treating absence of a tree as absence of a slow request. A metadata-only fallback for an oversized local message requires either module support or independently delivered event data; the daemon cannot extract a body it has already refused to read.

### 4. Tune the stock module to the desired diagnostic level

Start with `detail=1` for useful slow-request diagnosis and leave generic internal-function tracing off. If measurement shows unacceptable overhead, compare `detail=0` plus targeted custom tracers. Do not assume that a lower threshold or a short retention period reduces PHP CPU cost.

Use endpoint-specific thresholds through `web_transactions_apdex` where possible; for example a 0.125-second Apdex T gives a 500 ms trace threshold under `apdex_f`. Treat that as a policy example. A fixed 500 ms INI threshold is simpler but also affects background and deliberately slow operations unless separately configured.

Test a finite `max_segments_web` such as 10000-20000 for pathological requests. The source documentation's rough 400 bytes per segment implies about 4-8 MB of segment structures at those counts, but this excludes other request data, strings, attributes, allocations and concurrency. Check diagnostic losses and exclusive-time effects before enabling the cap generally.

Disable automatic SQL EXPLAIN unless plan capture is a specific requirement. Keep SQL obfuscated. The SQL stack and slow-query thresholds are separate controls: `nr_segment_datastore_stack_worthy` accepts the stack threshold **or** the slow-query threshold when slow SQL is enabled (`axiom/nr_segment_datastore.c:368`). Raising only the stack threshold may therefore fail to eliminate backtrace work. EXPLAIN is not needed to retain SQL timing and obfuscated query information.

An error that completes below the module's slow-trace threshold will not automatically produce a full slow transaction tree merely because our daemon prioritizes errors. Continue ingesting the independent error stream; lowering a capture threshold or explicit module-side instrumentation is required if a full tree for such requests is essential.

### 5. Use a lean custom export/storage representation

Preserve the PHP-facing FlatBuffers protocol. Change only daemon-to-backend export when removing collector-specific base64/nested compression. Keep legacy collector export as a separate compatibility path if needed.

Store each selected trace with typed summary columns (tenant, application, transaction name, start time, duration, GUID, trace ID where available, selection reason) and its compressed nested tree. Decode and enrich the tree once during ingestion or first use. Do not eagerly turn every trace node into a separately indexed database row unless actual queries require it.

For approximately seven-day retention, ordinary asynchronous expiration is acceptable. Align trace summaries, trees and derived data to the same retention policy. With a few clients, begin with one storage node; the trace findings do not establish a need for a cluster or message broker.

### 6. Make traces more useful without pretending missing data is present

Provide a waterfall and aggregate repeated observed SQL/external operations into counts, cumulative duration, maximum duration and query fingerprints. Highlight likely N+1 patterns, but label counts as observed when segment truncation may have occurred. Keep separate slow-SQL summaries; they are not a substitute for all SQL calls within one trace.

Calculate wall-clock self time using the union of relevant child intervals, not a naive sum that double-counts overlaps. Respect async contexts and distinguish these derived values from the module's CPU metrics. Because ancestors/children can be omitted and node times are millisecond-granular, derived self time and unexplained gaps are approximate. The tree cannot expose the full original segment count or all omissions unless another field/source supplies that information.

Keep slow-trace analytics separate from overall transaction latency metrics: a duration-biased exemplar pool cannot provide unbiased p95/p99 or an overall error rate. The six-value metric summaries also do not contain enough information to reconstruct exact percentiles. Use a suitable event sample or a deliberate histogram pipeline when percentile accuracy is required.

## Small confirmed implementation defect

`TxnTraceHeap.AddTxnTrace` (`txn_traces.go:99`) comments that the oldest trace wins when durations are equal, but rejects only strictly shorter arrivals. Equal arrivals pass `IsKeeper`, pop the previous heap root and insert the newcomer. At the normal capacity of one, the newest equal-duration trace replaces the older one.

Choose an explicit tie policy and test it. If oldest wins, reject equal arrivals consistently; if freshness wins, document that behavior. This is lower priority than trace diversity and failed delivery, but otherwise repeated equal-duration candidates also cause avoidable payload copies and heap replacements.

## Verification and limits

Examined instrumentation, finalization, segment serialization, local framing, daemon admission, harvest/export and error paths in the pinned source. Cross-checked the public [PHP configuration](https://docs.newrelic.com/docs/apm/agents/php-agent/configuration/php-agent-configuration/), [trace details](https://docs.newrelic.com/docs/apm/transactions/transaction-traces/transaction-traces-trace-details-page/) and [custom instrumentation](https://docs.newrelic.com/docs/apm/agents/php-agent/features/php-custom-instrumentation/) documentation. Precise implementation conclusions above use the local pinned source where general documentation is less specific.

Three isolated behavioral checks passed using the upstream heap implementation copied verbatim into a standard-library-only harness under `.analysis/trace-selection-check/`:

1. A 5-second route displaces a 900 ms route at capacity one.
2. Increasing capacity to three still permits one route to occupy all slots.
3. Equal-duration arrivals replace the earlier trace, contrary to the source comment.

These checks run with the installed Go 1.17 toolchain using a compatibility alias for `any`. They are not a build of the full upstream daemon, a PHP integration test, a compression benchmark or a production load test. The downloaded upstream checkout remains unchanged.

Before implementing the recommendations, capture representative workloads: many short DB queries, a single slow query, external timeouts, CPU-heavy PHP, overlapping fibers, errors above/below threshold, more than 2000 retained segments, large attributes, route bursts and backend outages. Measure PHP request CPU/RSS, worker throughput, shutdown time, candidate/retained counts, trace fidelity, daemon RSS, bytes exported and backend query latency.
