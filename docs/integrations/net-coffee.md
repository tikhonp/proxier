# Net.Coffee

The optional integration reads IP reputation from [ip.net.coffee](https://ip.net.coffee/). It starts disabled. Enabling it discloses observed proxy exit IPs to that service.

## Requests

| Purpose | Request | Route |
|---|---|---|
| Claude exit IP | `GET https://claude.ai/cdn-cgi/trace` | Through the selected endpoint |
| ChatGPT exit IP | `GET https://chatgpt.com/cdn-cgi/trace` | Through the selected endpoint |
| Raw IP reputation | `GET https://ip.net.coffee/api/iprisk/{ip}` | From Proxier with an explicit observed IP |
| OpenAI API reachability | `GET https://api.openai.com/` | Through the selected endpoint, without credentials |

Requests do not follow redirects. A trace must return HTTP 200 and one public `ip=` line. IPv4-mapped IPv6 addresses normalize to IPv4. Reputation responses must return a public IP and a numeric `trust_score` between 0 and 100.

Net.Coffee caches reputation by CIDR, so its response can name a different IP in the same range. When `cidr` is present, it must be a canonical, nonzero prefix containing both the requested and returned IPs. Without `cidr`, the IPs must match. Proxier keeps the observed exit IP in results and cache keys.

Missing or null reputation flags remain unknown. The parser retains `cidr`, `countryCode`, `is_datacenter`, `isResidential`, `is_vpn`, `is_proxy`, `is_tor`, `is_crawler`, `is_abuser` and `is_mobile`.

Successful lookups share a cache by exact IP for 24 hours. There is no CIDR cache in Proxier. A rate limit sets a shared persisted retry deadline. See [AI IP checks](../processes/servers/ai-ip-checks.md) for timeouts, retention and failure handling.

## Scores and country policy

The [Claude frontend](https://ip.net.coffee/claude/claude-page.js?v=20260916a) and [ChatGPT frontend](https://ip.net.coffee/gpt/gpt-page.js?v=20260916a) use the same raw reputation endpoint. They observe different service destinations. They do not return independent backend scores for Claude and ChatGPT.

Both frontends override the displayed score to zero for CN, HK, MO, RU, KP, IR, SY, CU, BY and VE. Proxier applies that policy to service scores, retains raw reputation and labels the override. The country policy lives in `aicheck.Eligibility` and follows those frontend sources as checked on 2026-11-15. It is not a complete provider eligibility policy. A country absent from this restriction list does not prove service availability. Missing or invalid country information produces no service score.

The IP reputation subscription option uses the Claude-observed exit and raw score. Proxier does not infer a Codex API exit IP from a ChatGPT trace. The separate OpenAI API check reports an HTTP response, including authorization and rate-limit responses, without claiming model access.

## Verified API behavior and limitations

An anonymous request to [`/api/iprisk/8.8.8.8`](https://ip.net.coffee/api/iprisk/8.8.8.8) returned HTTP 200 with raw score 63. It required no API key or session. The response included public cache headers and permissive CORS. Those headers establish no reuse permission.

The broader `/api/ip/lookup/{ip}` endpoint returned different VPN and abuse flags for the same IP. Proxier uses only `/api/iprisk/{ip}` to match the AI pages. It collects no browser fingerprint or session report.

No published automated-use permission, quota or integration contract has been found. [robots.txt](https://ip.net.coffee/robots.txt) excludes `/api/` from crawling. Supported production use, classification accuracy for ordinary proxy exits and client behavior when score suffixes change remain [external checks](../open-questions.md#ai-ip-reputation).
