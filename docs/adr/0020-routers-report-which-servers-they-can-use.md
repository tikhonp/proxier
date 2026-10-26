# A router reports which servers it can use, and its link leaves out the ones it can't

Status: proposed, not built.

[ADR 0005](./0005-home-is-the-russian-vantage-point.md) made Proxier's own network the ground truth for "usable from Russia". One network is not every network. A router at another address, on another provider, or only at another time of day can reach servers that Proxier's proxy test fails on, and the other way round. The router's mihomo container picks a server for every connection of the house, so the router's view is the one that decides whether that house has internet.

Seen on an RB5009 (RouterOS 7.25, mihomo v1.19.33), and again on a hEX with the same symptoms after its boot: a packet capture of 64 connections the container opened to its six servers showed 16 that completed the TCP handshake and then got no answer to any data. Which servers they went to changed within the hour. The router reached the same servers with a plain HTTPS request 120 times out of 120. On the same day three of the six servers were down for everyone (check-host.net reached none of them from eight countries), and Proxier's own health called all six down. mihomo's check marks a stalling node dead and alive again in the next gap, and each failed connection marks the current node dead too, so its url-test group walks through every node in turn and the router's DNS over HTTPS through the tunnel worked between 0 and 80 % of the time. A server that works for Proxier, for check-host.net or for another house can still fail for this one, and only this house can tell.

## Decision

A router is a vantage point.

1. A router that runs the mihomo container has an optional link: the link its container fetches. A member sets it on the router's page.
2. Proxier reads the router's mihomo provider state over the SSH connection it already has (`/tool fetch` of the container's controller, which listens on the LAN). It does this every minute for routers with a link, as a check job of its own, not inside a sync. Each node's last check results become check results with the vantage point `router:<id>`, matched to a server by the name Proxier gave that server in the link's output.
3. A server is bad from a router when at least 20 % of that router's samples of it in the last hour failed, with at least 30 samples. It is good again below 10 %. Both numbers are settings. The share is used, not mihomo's alive flag, because a node that stalls half the time is alive in the gaps.
4. The router's link leaves out the servers that are bad from its router, after the same grace period as hide unhealthy, and never all of them: if every server is bad, the link serves all of them and raises the existing all-unhealthy alert for that link. The link's subscription is not changed, and no other link is touched.
5. The server's health state stays what Proxier's own checks say. A router's view never turns a server `down` or `blocked` for everyone. The Health tab shows one more row group per router, and a router's changes of view are events.

## Considered options

- **Exclude servers in the router script** (mihomo's `PROVIDER_EXCLUDE_FILTER`): needs a script parameter per router and a person to know which servers to list. It is the manual fix again, held in a different place.
- **Better health checks inside mihomo**: a check that only completes a handshake passes in the gaps between stalls. A check that is long enough to catch the stall is a different load on every server from every router, and still decides inside the router, where Proxier can't see it.
- **The router calls Proxier** (a RouterOS script that posts results): needs a token on every router, a public endpoint that takes data from the router, and a script that has to run on routers of every version. Proxier already has an SSH connection to each router.
- **Trust Proxier's own proxy test alone**: it is one network. It also failed here while the servers worked for the router.

## Consequences

- The routing module owns routers and their SSH connections, so it runs the check and stores the samples. The subscriptions module decides what a link serves. They talk through a port (the subscriptions module asks whether a server is bad from a link's router) and an event, as in [ADR 0002](./0002-modules-talk-through-ports-and-events.md). Neither reads the other's tables.
- Routers without a link, or without the container, get no check. A link that no router is attached to behaves as today.
- The controller listens on the LAN without a password. Proxier reads only, and only over the router's own SSH session.
- A router whose container is down reports nothing. Proxier then keeps the last verdict for an hour and drops it afterwards, so a rebooting router does not hide every server.
- The names Proxier puts in a link's output become part of the contract with the router: renaming a server in a link's output makes its samples start over.
- A subscription link read by the router is hidden from a server only on that router's schedule: mihomo reloads its provider on its own interval, so a change reaches the container after it.

## Rejected

- **Making the router's view the server's health state**: one house's route would send notifications and hide the server from every other member's link.
- **Removing the server from the subscription itself**: the same subscription may feed links used from other networks, where the server works.

## Not yet known

- The thresholds above are guesses from one router's capture. That router's stalls also changed within an hour, so a window of one hour may be too long, and whether any set of servers is reliably good from a house is not shown yet.
- Whether the stalls are about the servers at all. From a laptop, on the home network and on a second one, a bare TLS handshake with the Chrome ClientHello that links ask for (`fp=chrome`) got through to three servers about one time in five, while Firefox, iOS and Edge hellos got through every time ([open questions](../open-questions.md#stalled-nodes-in-the-routers-mihomo)). If a link with another fingerprint stops the stalls on a router, this check matters less for them, but not for servers that are down or blocked from one house only. A second router, with one `fp=firefox` link among `fp=chrome` ones, did not separate them in a four-minute watch, so the question stays open.
- How many samples the controller keeps per node (mihomo keeps only a short history per node, a few minutes at the 20 s interval used here). A once-a-minute read sees every check; a slower read would miss some, so Proxier keeps its own count.
- Whether the response of a router with many nodes fits what `/tool fetch` returns to an SSH session. The six-node response was a few KB.
