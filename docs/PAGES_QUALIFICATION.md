# Proposed Pages qualification gate

This source proposal binds a static Status build to an explicitly selected
deployment record and preserves the exact built files across the build/deploy
boundary. It does not establish chain readiness. Maintainer adoption,
independent release/epoch evidence, effective environment protection and actual
deployment qualification are still required. No usable deployment record is
supplied by this proposal; missing configuration refuses publication.

## Trust and owner setup

Before enabling this workflow, authorized release and application owners must
review the actual chain-epoch release manifest and its qualification evidence.
Decide the accepted source and Core revisions, chain ID, epoch, public endpoints,
base path and validity interval. The evidence digests identify independently
accepted material; the checker does not fetch, authenticate or independently
review it. A fabricated record with administrator-selected matching pins is not
detected as fabricated. Protect repository variables, workflow/source write
access and the `github-pages` environment, including its allowed branches,
reviewers and bypasses, according to the accepted release policy.

Prepare the following record outside Git after the intended source commit
exists. Set the repository variable `PAGES_DEPLOYMENT_RECORD` to its exact UTF-8
bytes and `PAGES_APPROVED_DEPLOYMENT_SHA256` to the lowercase SHA-256 of those
same bytes. Whitespace and the final newline count. Do not put private paths,
personal data, tokens, credentials or non-public endpoints in this record: it
will be published. These two variables share the repository's configuration
authority; they are not an independent signature or review mechanism.

```json
{
  "schema": 1,
  "repository": "Zenoglyphs-VM/zvm-status",
  "sourceCommit": "<40 lowercase hex characters>",
  "coreCommit": "<40 lowercase hex characters from the source gitlink>",
  "basePath": "/zvm-status/",
  "chainId": "<canonical positive decimal uint64>",
  "chainEpoch": "<64 lowercase hex characters>",
  "releaseManifestSha256": "<64 lowercase hex characters>",
  "qualificationEvidenceSha256": "<64 lowercase hex characters>",
  "validFrom": "<YYYY-MM-DDTHH:MM:SSZ>",
  "validUntil": "<YYYY-MM-DDTHH:MM:SSZ>",
  "environment": {
    "VITE_ZVM_NODE": "https://node.example.invalid/zvm",
    "VITE_GATEWAY": "https://gateway.example.invalid",
    "VITE_NOM_NODE": "wss://nom.example.invalid/zenon"
  },
  "scope": "static-distribution-only; chain and wallet readiness require independent verification"
}
```

This is an intentionally invalid template. Replace placeholders with accepted
values; a green checker is still not independent acceptance of those values.
Keep `ZVM_NODE`, `ZVM_GATEWAY` and `NOM_NODE_WS` equal to the respective record
values. URLs must use HTTPS/WSS, without credentials, query data, fragments or
literal private addresses. DNS resolution and server behavior are not checked.
The record must satisfy `validFrom <= current UTC time < validUntil` at both
build and deployment. An expired artifact is refused even if it built earlier.
Rebuild for a changed commit, gitlink, endpoint, epoch, record or validity window;
do not edit a sealed output or rebind an old artifact to a new record.

## Workflow and artifact boundary

Only upstream `main` can enter the proposed Pages workflow. The unprivileged
build job first refuses absent/mismatched configuration, then runs the offline
gate controls, installs the locked frontend inputs, typechecks and builds. This
retains the original frontend build checks; it does not supply missing browser,
wallet, chain or application-semantic tests. The build then seals the completed
directory and passes two independent workflow outputs: its immutable artifact
ID and the SHA-256 of its complete file manifest.

The environment-protected deployment job downloads that exact artifact ID. It
installs no application dependencies and does not rebuild. It rechecks source,
Core, exact record bytes, configured endpoints, current validity and the complete
file/size/hash set against the manifest digest from the build job. Added,
removed or changed files, or a rewritten manifest, refuse publication. Only
after this check are the same directory bytes packaged for Pages and submitted
by the existing Pages deployment action. The workflow must remain trusted and
its successful build output must not be forged; the gate is not a sandbox
against an administrator or a concurrently hostile same-user process.

The intermediate artifact is retained for one day and uses no compression.
This handoff separates dependency installation from Pages write/OIDC authority.
The [upload](https://github.com/actions/upload-artifact) and
[download](https://github.com/actions/download-artifact) actions use their
explicit artifact ID interface; the final
[Pages action](https://github.com/actions/deploy-pages) consumes the selected
Pages artifact name. Actions are pinned to official full commits. No workflow
dispatch, repository variable, environment, branch rule or deployment is created
by adding these source files.

`pages-artifact.json` hashes every ordinary artifact file except itself. Its
own digest travels through the build output, never through a value read back
from the downloaded artifact. `deployment.json` retains the exact approved
record. `deployment.html` shows its public source, chain/epoch, evidence digests,
validity and evidence limits. Each built HTML route gains a visible link and
the intended epoch/expiry. This labels the intended build; it does not discover
a new live epoch, revoke a previously served page or authenticate an endpoint.
Operators still need a current independent deployment/epoch registry, freshness
monitoring and a supported-browser drill to identify every stale/wrong-epoch
served copy automatically. A static build must never be presented as chain,
wallet or production acceptance.

## Bounded offline controls

Run without dependency installation or network access:

```sh
python3 -B -m unittest discover -s tools/pages-gate -p 'test_*.py' -v
```

The suite covers exact approval bytes, missing/unknown/type-confused fields,
source/Core/repository/config mismatches, validity edges, unsafe endpoint
shapes, malformed/duplicate/oversized JSON, complete file-set and same-size
tampering, rewritten manifests, reserved output preservation, repeated seals,
visible metadata in both routes, limits and the command-line handoff/redaction.
The original `test_pages_gate.py` link, special-file and hard-link controls
use simulated stat metadata. The separate synthetic handoff controls below add
actual Linux fixture checks; other platforms remain unqualified. Generated
fixtures contain no credentials and contact no server.

The checker caps record/manifest bytes, artifact entries, total file bytes,
HTML size, path length and depth. It rejects links, special files, case aliases
and hidden paths. Hashing streams in bounded chunks. It detects observed file
changes, but supplies neither filesystem transactions nor power-loss durability.
An interrupted/failed seal is unqualified; preserve its failure evidence and
rebuild fresh scratch rather than retrying over partial outputs. Actual application
artifact transfer, environment review, Pages delivery, headers/cache behavior,
browser rendering, clock authority and independent chain evidence need separate
qualification. Other applications remain outside this initial D03 slice.

## Synthetic hosted artifact handoff

The separate Pages qualification workflow adds a bounded producer and consumer
using the unchanged production `pages_gate.py` CLI. The producer seals two small
synthetic HTML routes and a text fixture, uploads at most 32 KiB with the pinned
artifact action, and exports the immutable artifact ID, exact record bytes and
trusted record/manifest digests through job outputs. The consumer downloads only
that ID into a fresh directory and runs production verification. No artifact
name search, rebuild, application dependencies or Pages permission is involved.
The synthetic record uses reserved `.invalid` endpoints and artificial evidence
digests. It never reads repository deployment variables or supplies accepted
chain/release evidence.

The consumer requires the production CLI to refuse ten changed-file or binding
cases, including same-size tampering, a rewritten manifest and wrong independent
pins/source/Core/repository/environment. It also exercises actual Linux symlink,
root-link, hard-link and FIFO refusal against disposable fixture copies. The
transferred input remains unchanged. The expiry control calls the production
verification function at the record's exact `validUntil`; this is controlled-time
coverage, not a wall-clock expiry wait. Local directory-copy controls do not
establish hosted transfer; only a source-bound successful producer/upload and
consumer/download job pair can establish that limited handoff result.

These jobs have `contents: read` only, three-minute limits and one-day artifact
retention. This workflow exercises synthetic transport and checker integration.
It does not qualify the actual application build, accepted release/epoch record,
environment protection, Pages packaging/delivery, DNS, browser rendering, cache
revocation or production operation. The deployment workflow and production gate
remain unchanged. Expired intermediate artifacts must be reported unavailable;
an old run or local fixture cannot substitute for retained exact-run evidence.
