# Synthetic check: vjencanja-api

Created in Grafana Cloud, Testing & synthetics, Synthetics, Add new check, API endpoint (HTTP). Recreate from this page if the stack is ever rebuilt.

| Setting | Value |
|---|---|
| Job name | `vjencanja-api` |
| Target | `https://api.vjenchanje.com/` |
| Method | GET |
| Request header | `Origin: https://app.vjenchanje.com` |
| Valid status codes | 200 |
| Header validation | `Access-Control-Allow-Origin` matches regex `^https://app\.vjenchanje\.com$`, "Allow missing" off |
| SSL | fail if SSL is not present |
| Frequency | 60s |
| Timeout | 5s |
| Probe locations | 2 EU locations (for example Frankfurt and Paris) |
| Publish full set of metrics | on (needed for `probe_http_status_code` and `probe_ssl_earliest_cert_expiry`) |
| Alerting in the check UI | off (alerts come from `alert-rules.yaml`) |

Budget: 1 check x 2 probes x 60 runs/hour x 24 x 30 = about 86,400 executions per month.

If Cloudflare challenges the probes (status 403 in `probe_http_status_code`): on the Free plan, Bot Fight Mode cannot be skipped by a WAF rule, so turn Bot Fight Mode off for this zone. On Pro or higher, add a WAF custom rule that skips Super Bot Fight Mode only for requests carrying a secret header set in the check (for example `X-Monitor-Key: <random value>`). Never skip on User-Agent: it is trivially spoofed.
