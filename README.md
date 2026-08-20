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

Real environment variables take precedence over `.env`, and `.env` is never
loaded into the process environment, so the token stays out of any child
process. Both missing variables are reported at once.

## Run

```sh
go run .
```

```
level=INFO msg="configuration loaded" workspace=1201234567890123 out_dir=out
level=INFO msg="saved category" category=users count=3
level=INFO msg="saved category" category=projects count=2
```

Exit status is 0 only if everything succeeded.

## Output

One file per object, named by its gid:

```
out/
├── users/
│   └── 1201234567890123.json
└── projects/
    └── 1209876543210987.json
```

Re-running overwrites in place; nothing is deleted, so an object removed in
Asana leaves its file behind. `out/` is gitignored — **the export contains user
email addresses.** If you change `ASANA_OUT_DIR`, ignore that directory too.

Only fields worth keeping are requested. Asana silently ignores `opt_fields`
names it does not recognise, so each one is verified against the live API
rather than taken from the reference docs.

## Behaviour worth knowing

**Rate limits.** Requests are paced by a token bucket at 145 per minute,
just under the Asana free tier limit of 150 (`requestsPerMinute` in
`internal/asana/client.go`; paid plans allow 1500). A burst of one spreads them
evenly, and the margin is deliberate: pacing at exactly 150 puts a tick at both
ends of a closed 60 second window, which allows 151 requests inside it.

**Retries.** `429` waits exactly as long as `Retry-After` asks. `5xx`,
transport failures and truncated bodies get a growing backoff, up to three
attempts. Malformed JSON, type mismatches and other `4xx` are final. A `401`
says to check `ASANA_TOKEN`; a `402` means a requested field needs a paid plan.

**Partial failure.** A category that fails does not stop the other, so a broken
projects fetch still leaves the users on disk. Both failures are reported
together and the exit status is still non-zero — read the counts to see what
landed. Ctrl-C stops the run rather than starting the next category.

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
