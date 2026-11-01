# Independent monitoring

Run an independent observer on another host and network. A process beside the
gateway cannot notify anyone when its own machine or network is unavailable.
Moving the status page to static hosting alone does not establish independent
probing or notification delivery.

The status service exposes a versioned `/healthz` response with `schema: 1`,
`ok` and `monitoring`. An external HTTP monitor must treat an unavailable
endpoint or any non-200 response as a monitoring failure. The response uses
`Cache-Control: no-store`.

A completed round is fresh for twice the configured `-every` interval plus
one minute. The default is eleven minutes. Before the first completed round,
after the deadline, on a backward clock, on a history-write failure, after
the watcher stops, or after an observed notification failure, health is 503.
This measures the monitoring process. A fresh round may correctly report a
monitored service as down.

The summary includes the same deadline. Conditional validators change when
monitoring expires or the relay state changes. The browser also expires its
retained copy locally, even if successful requests keep returning the same
body. Historical observations remain visible, with current health unverified.

## One-shot observer

The executable includes an independent age check:

```sh
./zvm-status observe -url https://status.example.invalid/healthz -max-age 11m
```

Replace the reserved example URL with the explicitly approved monitoring
endpoint. A supervisor on the independent host should run this command and
alert on nonzero exit. It must also detect a missed observer execution; a
command that is never run cannot report its own absence. No scheduler or
notification destination is installed by this program.

The command refuses redirects, unavailable/non-200 endpoints, unsupported or
oversized responses, expired evidence, future timestamps and a declared
freshness window larger than its own age budget. Use an explicit matching
budget when changing the server interval. Keep observer and server clocks
synchronized. Endpoint identities and credentials are omitted from errors.

## Notification evidence

`notifications.state` distinguishes `disabled`, `unverified`, `pending`,
`accepted` and `failed`. Configuration alone is unverified. A successful
no-op probe does not clear a failed attempt. SMTP greeting, TLS negotiation,
commands, body and final acceptance share a 30-second maximum deadline.
Only one relay attempt is in flight; an overlapping alert is reported as a
failure instead of spawning another sender. Incident suppression remains in
force; this is not a durable retry queue or guaranteed redelivery.

An observer can additionally require a recent observed relay acceptance:

```sh
./zvm-status observe -url https://status.example.invalid/healthz \
  -max-age 11m -require-relay-acceptance
```

This intentionally fails until an actual relay acceptance has been observed
by the running status process. An acceptance means the relay queued a
message, not that the recipient received or read it. Restart resets this
in-memory evidence to unverified. A separate `-test-alert` process does not
establish a receipt for the running process.

## Acceptance drill

Before adopting an observer, record its independent host/network placement,
exact binary/source and endpoint configuration, permitted clocks and age
budget, and approved notification path. Exercise watched-host loss, a stopped
prober with an HTTP server still answering, stale/replayed responses, relay
refusal or stalled acceptance, and a stopped observer. Verify notifications
at the intended recipient using operator-approved test messages. Retain
failure, recovery and missed-run evidence.

Local HTTP/SQLite/SMTP fixtures establish selected code behavior. They do not
prove independent placement, actual host death, recipient delivery, production
operations or activation. Those require a separately authorized operator drill.

## Bridge complete sweep admission

An operational bridge observation requires a completed, non-running sweep and
all five critical steps: receive_deposits, scan_withdrawals, pay_withdrawals,
confirm_payouts and fund_plasma. Every step must have succeeded without an error,
have ordered start/finish timestamps and finish no later than the completed
sweep. lastOkAt and lastSweepAt must name the same completion second. The
existing ten-minute last-success limit and one-minute future-clock allowance
remain in force. Missing, extra, failed, canceled or incomplete step evidence
cannot produce operational status, even when healthy is true.

Upgrade this prober together with the Bridge complete-sweep producer. Legacy
aggregate-only reports are degraded until the producer supplies the contract;
no historical success is fabricated. These read-only HTTP observations do not
prove independent chain authority, successful payouts or off-host notification
delivery. External deployment and operator acceptance remain separate gates.


### Bounded Bridge command integration

The Linux `bridge-process` CI job pins the companion Bridge source and its Core
gitlink, builds both production commands with race instrumentation, and launches
the actual `zvm-bridge bridge` and `zvm-status -once` entrypoints as separate
processes. A private SQLite file and loopback-only NoM/EVM read fixtures exercise
complete success, a failed critical step followed by a later no-op, an in-progress
sweep with a retained previous success, complete recovery, and a stopped producer.
Unexpected RPC methods fail the test; no transaction publication is permitted.
Missing executable inputs fail the dedicated tagged lane instead of skipping it.
Every child has a deadline, capped output and an explicit join.

This is controlled Linux process integration. Other Status component verdicts,
actual NoM/execution-client agreement, real deposits/payouts or committee custody,
independent off-host monitoring, recipient delivery, deployment and operator or
maintainer acceptance remain separate. The public devnet configuration is fixture
input, not production configuration or chain authority.
