# build-monitor

A CircleCI build radiator served from a local Go process. See README.md
for what it is and how to run it.

## Shape of the code

- `cmd/build-monitor` — entrypoint; loads config, starts poller and server.
- `internal/config` — YAML config, token loading, exclude globs.
- `internal/circleci` — minimal v2 API client. Pipelines and workflows only.
- `internal/monitor` — polls on an interval, rolls workflows up per
  project, holds the current board behind an RWMutex.
- `internal/web` — HTTP server; static assets are `go:embed`ed so the
  binary is self-contained.

## Things that will bite you

**The v2 pipeline endpoint returns no `vcs` block.** For GitHub App
projects, branch/SHA/commit live under `trigger_parameters.github_app`.
Older webhook-triggered projects return `trigger_parameters: {}` and have
no branch at all — they are deliberately kept on the board, not filtered.

**A pipeline can fail without producing any workflow.** When the config
cannot be fetched or parsed, `state` is `errored` and `errors` is
populated, but `/workflow` returns an empty list. Rolling up only
workflow status paints these grey, which on a radiator reads as "no
news" rather than "broken". `Pipeline.Errored()` exists for this.

**Wall-clock duration counts approval waits.** A workflow parked on a
manual `approve` job is not running, but the timestamps say it is -- one
real pipeline here reported 7.2 hours, of which 25,519 of 25,996 seconds
were the approval gate. `buildSeconds()` recomputes from job times when
wall clock exceeds `heldDurationThreshold`, skipping `approval`, `lock`
and `unlock` job types. It is lazy on purpose: the extra requests happen
only for runs whose number is already implausible.

**Insights endpoints back the progress bar and the broken-for counter.**
`/insights/{slug}/workflows` gives median durations in one call per
project and is cached for `durationTTL`, since medians move slowly.
`/insights/{slug}/workflows/{name}` gives run history and is fetched only
for projects that are currently red, so that cost scales with breakage
rather than fleet size.

**Statuses roll up by worst-wins.** See `severity()` in
`internal/monitor`. Adding a status means placing it in that ordering,
not just mapping it.

**The token never reaches the browser.** The page talks only to the
local server. Keep it that way — do not proxy CircleCI responses
verbatim if they would ever carry credentials.

**The favicon is generated, not just served.** `static/favicon.svg` is
the static fallback; `paintFavicon()` rebuilds the mark as a data URI on
each render so the tab reflects board state. Keep the two in visual sync
if the mark changes.

**The logo carries its own theme switch.** `docs/logo.svg` sets the
wordmark via an embedded `prefers-color-scheme` block, because an SVG
loaded through `<img>` is style-isolated from the page around it and
GitHub renders READMEs in both themes. Light is the default branch so a
viewer reporting no preference still reads.

## Conventions

- `gofmt` and `go vet` clean before committing.
- The feed always emits `projects` and `errors` as arrays, never `null`,
  so clients need not distinguish empty from absent.
