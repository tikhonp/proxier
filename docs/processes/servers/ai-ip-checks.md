# AI IP checks

**Actors**: a member with `servers: manage`, the servers module, endpoint proxy dialers, Claude, ChatGPT, OpenAI API and Net.Coffee.

These optional checks compare the reputation of endpoint exit IPs. They do not decide server health or prove access to a model. See [Net.Coffee](../../integrations/net-coffee.md) for the API and country policy.

## Steps

1. Enable Net.Coffee checks in Settings > Servers. The setting explains that lookups disclose observed exit IPs and that automated-use terms and quotas remain unconfirmed. Both the integration and daily refresh start off.
2. On an active server's Health tab, select **Check AI services**. Proxier queues `servers.ai_check` on `server:<id>`. Repeated requests coalesce while the job has not started. This action is separate from **Run checks now**.
3. Open each endpoint with its current connection settings. Request Claude's and ChatGPT's trace URLs through that endpoint, without a direct fallback or redirect. Validate the returned public IP and record its address family and observation time.
4. Reuse a successful lookup for the exact IP for 24 hours across endpoints and services. Otherwise request `/api/iprisk/{ip}` from Net.Coffee, outside a transaction. A 429 sets a shared, durable cooldown from `Retry-After`, defaulting to one hour when absent or invalid. Transient failures get at most one retry after 250 ms. Requests time out after 20 seconds. Trace bodies are limited to 8 KiB and reputation bodies to 64 KiB.
5. Commit the attempt, any cache update and any cooldown with `server.ai_check_completed`. Keep the latest attempt separate from the latest successful lookup and latest exit observation. Failures never become zero scores.
6. Request the OpenAI API root through the endpoint. Record its HTTP status separately, without an exit IP or score. A response below 500 reports HTTP reachability, including 401, 403 and 429. It does not establish authorization or model access.
7. Show results on Health. The AI IP ranking page sorts each selected mode by fresh results with known country policy, fresh unknown-country reputation, then stale or missing results. Scores sort descending within each fresh group, with server name and endpoint key as tie breakers.
8. Each subscription may add a fresh cached score to output display names. No output or preview request makes remote calls. An optional daily refresh queues active servers and skips a busy resource. Daily maintenance removes attempts and cache rows older than 30 days, even with the integration off. A nonempty cleanup records `server.ai_history_pruned`.

## Rules

- The raw reputation is shared for an IP. Claude and ChatGPT observe exits separately and apply Net.Coffee's country override separately. An unknown country yields no service score. IP reputation uses the Claude observation without an override.
- Fresh output requires a successful observation and lookup less than 48 hours old for the current endpoint generation. A failed refresh may retain that success until expiry. A newly observed, different IP invalidates the old score even if its lookup fails. A later trace failure or return to the original IP cannot restore it without a successful lookup.
- A generation includes the endpoint row, its encrypted connection settings and update time, the server IP and IP-change time, and additional addresses. Changes invalidate output immediately. Late results for an earlier generation remain history. Retirement prevents publication of results from an in-flight check.
- Checks use the existing server access level for diagnostics. Subscription-only members receive usable scores through `EndpointCatalog`, without unrestricted server diagnostics.
- No health verdict, provisioning decision, previous-IP check, subscription order, membership or hiding decision depends on these scores. No new notifications are on by default.
- Cancelling a running job cancels remote requests. An interrupted AI check does not resume after restart. A member may queue another check.

## Edge cases (each is a test)

- A failed proxy dialer records failed attempts and makes no direct trace request or reputation lookup.
- Different endpoint exits retain separate results. Shared exits use one reputation lookup, without sharing endpoint reachability.
- A restricted country preserves raw reputation but yields a service score of zero. Missing country information retains the raw score and yields no service score.
- IPv4 and IPv6 exits retain their family. Private, shared, link-local, malformed or duplicate trace IPs are rejected.
- A CIDR-cached reputation response can name another public IP within the reported range. Both IPs must belong to that canonical, nonzero prefix. Results and cache keys retain the observed exit IP, and different exits in one range still have separate cache entries.
- Malformed JSON, missing, null, string or out-of-range scores, malformed or unrelated CIDRs, mismatched IPs without a CIDR and oversized bodies fail without accepting a reputation result.
- A failed refresh retains a fresh previous success. At 48 hours it expires. An exit change followed by failed lookup, return to the original IP and failed trace never restores the old score.
- A 429 pauses lookups across endpoints and later checks until its retry deadline. Transient failures retry at most once.
- IP, endpoint or additional-address changes invalidate output. A late result from the old configuration cannot become current. Retirement prevents queued checks and in-flight result publication.
- Daily refresh starts off. Enabled refreshes coalesce, manual requests coalesce with queued refreshes, and disabled integration serves no scores or new checks.
- Daily cleanup removes expired history and cache while the integration is off. Empty cleanup records no event.
- Members without `servers: manage` cannot open ranking or Health diagnostics or queue checks. Subscription fetches and previews use cached scores and disclose no diagnostics.
