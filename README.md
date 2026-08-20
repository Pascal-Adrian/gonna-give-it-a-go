# gonna-give-it-a-go

Extracts every user and project from one Asana workspace and writes each object
to its own JSON file.

## Setup

Requires Go 1.26 or newer.

```sh
cp .env.example .env
```

Fill in the two required variables:

| Variable | Required | Default | |
|---|---|---|---|
| `ASANA_TOKEN` | yes | | Personal access token: Asana > My Settings > Apps > Personal access tokens |
| `ASANA_WORKSPACE_ID` | yes | | Workspace gid, digits only. List yours at `https://app.asana.com/api/1.0/workspaces` |
| `ASANA_OUT_DIR` | no | `out` | Where the JSON is written |
| `ASANA_POLL_PROJECTS` | no | `30s` | How often projects are re-read |
| `ASANA_POLL_USERS` | no | `5m` | How often users are re-read |
| `ASANA_RATE_LIMIT` | no | `150` | Requests per minute your plan allows |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |

Real environment variables take precedence over `.env`, and `.env` is never
loaded into the process environment, so the token stays out of any child
process. Both missing variables are reported at once.

## Run

```sh
go run .
```

```
level=INFO msg="configuration loaded" workspace=1201234567890123 out_dir=out ...
level=INFO msg="saved category" category=users changed=3 unchanged=0
level=INFO msg="saved category" category=projects changed=2 unchanged=0
level=INFO msg="saved category" category=projects changed=0 unchanged=2
```

It keeps running, re-reading projects every 30s and users every 5m, until
interrupted. Ctrl-C stops it cleanly. Every poll is logged, including the ones
that found nothing new, so an idle poller is visibly alive rather than merely
silent; raise `LOG_LEVEL` above `info` to quieten it.

Each category polls on its own schedule, in its own goroutine. That is what
keeps them independent: neither can delay or fail the other.

It is not a speed optimisation on the free tier. Pages within a category are
chained -- each one needs the previous page's offset -- so a category issues at
most one request per round trip, about 3 per second. Two categories together
demand roughly 6. At 150 requests per minute the bucket only allows 2.4 per
second, so it, not the network, sets the pace and running them at once saves
nothing. Above roughly 400 requests per minute the bucket stops binding and the
same code halves a full sweep; on a paid plan at 1500 it allows 24 per second,
far more than the categories can ask for.

A file is only rewritten when its content actually changed, so modification
times stay meaningful for anything watching the directory.

## Output

One file per object, named by its gid:

```
out/
├── users/
│   └── 1201234567890123.json
└── projects/
    └── 1209876543210987.json
```

Files are overwritten only when their content changed; nothing is ever deleted,
so an object removed in Asana leaves its file behind. `out/` is gitignored — **the export contains user
email addresses.** If you change `ASANA_OUT_DIR`, ignore that directory too.

Only fields worth keeping are requested. Asana silently ignores `opt_fields`
names it does not recognise, so each one is verified against the live API
rather than taken from the reference docs.

## Behaviour worth knowing

**Rate limits.** Set `ASANA_RATE_LIMIT` to what your plan allows -- 150 free,
1500 paid -- and the client paces just under it. Asana returns no rate-limit
headers at all, so the number cannot be discovered; making it configuration
means a change to Asana's limits is a restart, not a release. The margin is
deliberate: a token bucket at exactly the limit puts a tick at both ends of a
closed 60 second window, which permits one request more than allowed.

At the defaults the tool costs about 2.2 requests per minute, roughly 1.5% of
the free tier budget.

**Retries.** `429` waits exactly as long as `Retry-After` asks. `5xx`,
transport failures and truncated bodies get a growing backoff, up to three
attempts. Malformed JSON, type mismatches and other `4xx` are final. A `401`
says to check `ASANA_TOKEN`; a `402` means a requested field needs a paid plan.

**Failure.** A poll that fails is logged and retried on the next tick, so a
transient 500 or a dropped connection does not end the process. The two
categories are separate goroutines, so a projects fetch that keeps failing
never delays or stops the users poll.

A credential that stops working is different: a `401` or `403` cannot be
retried into working, so it ends the run and exits non-zero rather than
logging forever while producing nothing.

Within a category, a save failure ends that poll — the causes are systemic, a
full disk or bad permissions — and the count of what landed first is logged.

## Layout

| Package | |
|---|---|
| `main.go` | wiring: config to client to store to service |
| `internal/config` | environment and `.env` |
| `internal/asana` | API client: pacing, retries, pagination, models |
| `internal/store` | one JSON file per object |
| `internal/extract` | orchestration; owns the `Source` and `Store` interfaces |

`internal/extract` depends on interfaces it declares itself, so the run is
testable with fakes and neither adapter knows about the other.

## Development

```sh
gofmt -l .
go vet ./...
go test ./... -race -cover
```

No test touches the network or the real filesystem outside `t.TempDir()`.
