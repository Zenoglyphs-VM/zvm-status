# zvm-status guide

Whether the public endpoints of a ZVM gateway are answering, checked
every five minutes, kept for thirty days, and emailed to somebody when one of
them stops.

Two pieces in one repository, because they are one thing:

- a Go service that probes, stores and alerts, and serves its history as JSON
- `web/`, the page at the gateway's `/status/` that draws it, and at
  `/status/rpc/` the endpoints and settings for adding the chain to a wallet
  (Rabby, MetaMask, Syrius); one app, the path picks the view

```sh
cp .env.example .env && set -a && . ./.env && set +a
go build ./...
./zvm-status -once                    # print one round and exit
./zvm-status -db status.db
cd web && npm install && npm run dev  # http://localhost:47500/status/
```

No target is built in. The prober reads them from flags that default to
`STATUS_ORIGIN` (the gateway, as this process reaches it), `STATUS_HOST`,
`STATUS_EDGE`, `STATUS_RELAYER`, `STATUS_BRIDGE`, `STATUS_BOTS` and
`STATUS_PUBLIC`; the page
lists for wallets the `VITE_ZVM_NODE`, `VITE_NOM_NODE` and `VITE_GATEWAY` it was
built with. `.env.example` has a local zvm-devnet's.

It runs as a container beside the gateway (`docker build -t zvm-status .`), as
zvm-devnet runs it. On devnet.zenon.foo, where it was first deployed, it ran the
same way, and `operations/PUBLIC.md` in zvm-docs is that deployment's
procedure, including the SMTP settings.

## What it checks

Seventeen components, grouped the way a visitor would name them:

| Component | Probe | Up means |
|---|---|---|
| Explorer, Glyphswap, Karum, Mozaic, zchan, ZNS, Spark, and Rain and Bullet when named in `STATUS_BOTS` | `GET /<app>/` and its entry script | the bundle is served, not just a 200 (Rain's, Bullet's and zchan's pages are plain HTML, so they are recognised by the rain canvas, `main#bullet` and the post form's slot rather than `#app`) |
| ZVM JSON-RPC | `eth_blockNumber` on `/zvm/rpc` | it answers, and the head is moving |
| ZVM API | `/zvm/api/info` | it answers with a chain id |
| Faucet | `/zvm/api/faucet` | it answers with its address |
| NoM JSON-RPC | `ledger.getFrontierMomentum` on `/zenon` | it answers, and momentums are moving |
| NoM WebSocket | the same call over `wss /zenon` | the upgrade works and a reply comes back |
| Relayer | its own `/status` | the backlog drains, and no new publish errors |
| Bridge | its own `/status` | its last vault sweep worked, and one worked in the last 10 min |
| Cloudflare edge | `GET /zvm/healthz` on the public URL | the tunnel is up |

The relayer and bridge have no public path, so they are asked for their own
`/status` by name on the network they share (`-relayer`, `-bridge`). They are on
the page because they are what a user is actually waiting on when a
transaction or a withdrawal does not arrive — and the RPC answers perfectly
well while either is stuck. A relayer is graded on *movement*: something queued
and nothing posted for three minutes is degraded, because the failure this
stack has had (zvm-fleet's livelock) is a backlog that stops draining while
`/status` keeps answering 200. The bridge is graded on its own `healthy` flag
(the last sweep worked), and a last success older than ten minutes is down
whatever the flag says, since a stopped loop cannot report itself.

Everything else is asked of the **gateway**, directly on the network they
share, with the public `Host` (`-host`). That is deliberate: if the probes went
through Cloudflare, a tunnel outage would paint every row red and say nothing
about which service was actually broken. Instead the edge is its own row, and
it is the one that goes red when the problem is between the internet and this
machine.

Three states, and each is a word and an icon as well as a colour:

- **operational** — answered, correctly, inside `-slow` (3 s).
- **degraded** — answered, but slowly, or answered while the chain is not
  advancing. A node that has dropped off gossip keeps serving RPC happily; the
  height not moving for three minutes is what catches it. Degraded shows on the
  page and emails nobody.
- **down** — did not answer, answered wrongly, or timed out — twice, twenty
  seconds apart. One dropped connection through two proxies is not an outage,
  and alerting on it is how an alert address gets ignored.

A day's bar is coloured by that day's uptime, not by its single worst check:
at 100% it shows the day's worst state, from 95% up to 100% it shows
degraded, and under 95% it shows down. One failed check in a day of 288 is
99.65% uptime, and a red bar for that said "outage" about a day that was
almost entirely up. The bar's tooltip still lists how long the service was
down and how long it was degraded.

## The email

One email per round, not one per component, because when Docker stops eleven
things fail at once:

```
Subject: [ZVM status] DOWN: Explorer, Glyphswap and 6 more

Down:

  Explorer — connection refused (/explorer — blocks, transactions, momentums)
  …
```

and one when they come back. Configure the relay through the environment:

```
SMTP_HOST, SMTP_PORT (587 default, 465 = implicit TLS), SMTP_USER, SMTP_PASS,
SMTP_FROM, ALERT_TO (comma-separated for several; no default)
```

`./zvm-status -test-alert` sends one message and exits — do that before
trusting it. With `SMTP_HOST` unset, outages are still recorded and the page
says alerts are not configured.

It has to be a relay with a login rather than direct delivery: this machine's
default route is a VPN, and VPN and residential exits are blocked on port 25 by
most providers and distrusted by the rest.

## The history

SQLite, one row per check: 30 days × 288 rounds a day × 11 components, about
95,000 rows, which is a few megabytes. Every number the page shows — a day's
uptime, the 24-hour latency line, an incident's duration — is computed from
those rows per request, so there is no rolled-up copy that can disagree with
them. Rounds land on the wall-clock grid (`:00`, `:05`, …) so a day is 288
slots however often the process restarts.

"Per request" means per round in practice. The API has two routes:

| Route | What | Size |
|---|---|---|
| `/api/summary` | every component's current state, 30 daily bars, incidents | ~31 KB |
| `/api/recent/{id}` | one component's checks over the last 24 h, for its chart | ~14 KB |

Both carry an ETag naming the last round and the UTC day, and both are computed
once per ETag and reused. A tab that already has the current round gets a 304.
The page asks just after each round boundary, every 5 s until that round
appears, then once a minute at most until the next one, and not at all while
hidden. The latency series was 83% of the summary when it was part of it; it is
now fetched only for a row someone opens.

An incident opens on the first bad check for a component and closes on the
first good one; it is what the page lists and what the email is keyed to, so a
component that stays down is one email and not one every five minutes.

## What it cannot tell you

**That the machine is off.** The prober runs on the machine it watches, so when
that machine sleeps, or Docker stops, nothing checks and nothing emails —
the page simply stops gaining bars, and the gap is visible as days with no
data. Catching that needs a second prober somewhere else; the honest version of
this page says so rather than implying silence means health.
