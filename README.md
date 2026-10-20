# zvm-status

Whether the public endpoints of a ZVM gateway are answering: checked every
five minutes, kept for thirty days, and emailed to someone when one stops.

- a Go service that probes, stores and alerts, and serves its history as JSON
- `web/`, the page at the gateway's `/status/`, with the wallet
  settings for the chain at `/status/rpc/`

The page compiles `@zvm/web` from the [zvm-core](https://github.com/Zenoglyphs-VM/zvm-core)
submodule, so clone with `--recurse-submodules`.

```sh
cp .env.example .env && set -a && . ./.env && set +a
go build ./...
./zvm-status -once                    # one round, printed
./zvm-status -db status.db            # probe, store, serve /api, alert
docker build -t zvm-status .
cd web && npm install && npm run dev  # http://localhost:47500/status/
```

No node is built in: the prober takes its targets from `STATUS_ORIGIN` (the
gateway, as it reaches it), `STATUS_EDGE`, `STATUS_RELAYER` and
`STATUS_BRIDGE`, and the page the `VITE_*` endpoints it lists for wallets.
The Telegram bots' pages are checked only when `STATUS_BOTS` names them
(`rain,bullet`), since a stack without a bot token does not serve them.
Running beside the gateway, it probes it directly rather than through the CDN,
so a tunnel outage shows as one row (the edge) rather than every row. A check
must fail twice, twenty seconds apart, to count as down.

Alerts need an SMTP relay: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`,
`SMTP_FROM`, `ALERT_TO`. `./zvm-status -test-alert` sends one message and exits.

The prober runs on the machine it watches, so it cannot report that machine
being off: the page just stops gaining bars.

## Deploy the page

Every push to `main` publishes `web/` to GitHub Pages (Settings, Pages, Source:
GitHub Actions), built with the organisation's `ZVM_NODE`, `ZVM_GATEWAY` and
`NOM_NODE_WS` Actions variables. That copy reads the live API under
`ZVM_GATEWAY`, at `/status/api/`.

MIT licence.

The full guide, with how it works and why: [docs/GUIDE.md](docs/GUIDE.md).
