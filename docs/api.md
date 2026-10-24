# HTTP API and metric semantics

One Go process serves the UI and API. Use HTTPS for public deployment.

## Dashboard access

`POST /api/login` accepts `{"token":"<APM_ADMIN_TOKEN>"}` and sets an eight-hour HttpOnly cookie. `POST /api/logout` clears it. Cross-origin browser submissions are rejected. Tools may instead send `Authorization: Bearer <APM_ADMIN_TOKEN>` on dashboard GET requests.

The token is administrator access across configured clients. Ingestion keys cannot access dashboard queries; per-client viewer accounts are not implemented.

## Reads

| Endpoint | Result |
| --- | --- |
| `GET /healthz` | Minimal, unauthenticated storage health; 200 or 503 |
| `GET /api/status` | Source, retention, collector hostname |
| `GET /api/applications` | IDs, names, tenants, last observed harvest |
| `GET /api/dashboard` | Summary, ranked components and chart series |

Dashboard parameters: `app=<id-or-all>`, `kind=plugin|hook`, `from=<unix-seconds>`, `to=<unix-seconds>`, and optional `name=<component>`. Defaults: all apps, plugins, last hour. The `[from,to)` range is limited to seven days. Buckets are 60/300/900/3600 seconds depending on window length.

Chart series contain **self milliseconds per minute**, and `call_rates` contain callback calls/minute. Partial buckets use their overlap with the query window as the denominator. Entire harvests are assigned to their end-time bucket; the API does not reconstruct request-level timestamps.

Summaries/rows expose calls, total/self milliseconds, average duration and maximum duration; row `share` is the fraction of this family's self time. Average duration is total duration / total calls, not an unweighted average of host averages.

## Native ingestion

`POST /v1/metrics` requires `X-APM-Ingest-Key`. The server assigns tenant identity from this key. JSON may be uncompressed, gzip, or zlib (`Content-Encoding: deflate`).

```json
{
  "application": "My WordPress site",
  "host": "web-01",
  "start": 1792886400,
  "end": 1792886460,
  "metrics": [
    {
      "name": "Framework/WordPress/Plugin/woocommerce",
      "values": [120, 2.4, 1.2, 0.002, 0.12, 0.1]
    },
    {
      "name": "Framework/WordPress/Hook/init",
      "values": [150, 3.0, 1.5, 0.001, 0.12, 0.12]
    }
  ]
}
```

Replace example timestamps with current ones. Values are **count, total seconds, exclusive/self seconds, minimum seconds, maximum seconds, sum of squared durations**. `scope` is optional. Other metric names are also retained but are not graphed here.

Success is 200 with `accepted` and `batch_id`, after ClickHouse's async insert flush succeeds. Identical batches are deduplicated in queries; metric ordering is canonicalized and repeated names/scopes within a batch are merged. Keep application/host/timestamps/content unchanged on retries and use distinct host identities for independent producers.

Limits: 20,000 metrics, 16 MiB compressed/decompressed body, eight ingestions in flight. Values must be finite/nonnegative. End time must be within retention with at most five minutes of future skew; one harvest interval cannot exceed one day. Input errors return 400; invalid keys return 401; overload/storage failure returns 503 and requires retry with backoff. There is no ingester disk spool.

## Existing daemon compatibility

`POST /agent_listener/invoke_raw_method?marshal_format=json&protocol_version=17&method=<method>&license_key=<client-key>[&run_id=<run>]`

`preconnect`/`connect` return collector envelopes and signed run IDs bound to key, tenant, primary app and host. Invalid/mismatched/expired runs return 409 to trigger reconnect. Ordinary service restarts preserve runs when signing configuration remains unchanged.

`metric_data` accepts `[run_id,start,end,[[descriptor,six_values],...]]` and stores typed rows. The following methods store their run-prefixed JSON argument arrays in the auxiliary payload table: `transaction_sample_data`, `error_data`, `sql_trace_data`, `analytic_event_data`, `error_event_data`, `span_event_data`, `custom_event_data`, `log_event_data`, `update_loaded_modules`.

Archival is not a claim that the UI implements each APM view. Unknown methods and policy-token/high-security registration are rejected. The handshake disables sampled event reservoirs for this dashboard; trace/error/slow-SQL collection is enabled for archival. Disable browser monitoring in the PHP configuration. Only the primary application name is used; rollup aliases are not emulated.

## Interpretation

WordPress emits both `Framework/WordPress/Plugin/` and `Framework/WordPress/Hook/` metrics. Themes share the plugin metric family. Only unscoped metrics feed these graphs to avoid counting scoped copies again.

Total duration includes nested work; self time excludes nested instrumented segments and is not CPU time. Hooks/plugins can describe the same work: do not add their totals or call callback throughput HTTP-request throughput. Aggregate metrics do not supply a per-plugin/per-hook cross-tab, complete request waterfall, or exact latency percentiles.
