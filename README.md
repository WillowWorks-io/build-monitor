# build-monitor

A CircleCI build radiator: one page, readable from across the room, that
replaces a browser tab per org.

Five CircleCI "All pipelines" tabs cost about 1.6 GB of Chrome. This
serves the same information from a single Go process holding roughly
15 MB, in one tab you can throw on a spare display and stop thinking
about.

## Design

It is a *radiator*, not a dashboard. Colour carries the signal and text
is secondary: failures sort to the top-left where the eye lands, a
running build breathes so motion rather than reading tells you it is
live, and tile sizes scale with the viewport so the board fills whatever
display it lands on.

The CircleCI token is read from disk by the server and never reaches the
browser. The page talks only to `localhost`.

## Running it

    cp config.example.yaml config.yaml   # edit orgs, then:
    go run ./cmd/build-monitor -config config.yaml

Open <http://127.0.0.1:8770>.

### Configuration

| Key | Meaning |
| --- | --- |
| `token_file` | Where to read the CircleCI personal API token. |
| `listen` | Address to serve on. Keep it on loopback. |
| `poll_interval` | How often to poll CircleCI. Floor of 15s. |
| `branch_filter` | `default` watches each repo's default branch; `*` watches every branch, like CircleCI's "All" tab. |
| `exclude` | Glob patterns matched against `org/project`. |
| `orgs` | CircleCI org slugs, e.g. `gh/vibrant-wozniak`. `bb/` for Bitbucket. |

`branch_filter: default` is the classic build-monitor semantic — it
answers "is trunk green?" without feature-branch noise. Switch it to
`"*"` to mirror what the CircleCI web UI shows.

## Status mapping

A project's tile shows the **worst** status across the workflows of its
latest pipeline, so one failed workflow colours the tile red even when
its siblings pass.

| CircleCI | Tile |
| --- | --- |
| `success` | passed |
| `failed`, `error`, `failing`, `unauthorized` | failed |
| `running` | running |
| `on_hold` | on hold |
| `canceled`, `not_run` | unknown (grey) |

A pipeline whose `state` is `errored`, or that carries entries in
`errors`, is reported as **failed** with the reason on the tile. Such a
pipeline never produces workflows at all, so a rollup reading only
workflow status would paint it grey — which on a wall display reads as
"no news" when it actually means the build is broken.

## API shape notes

The v2 pipeline endpoint returns **no `vcs` block** for GitHub App
projects. Branch, SHA and commit metadata live under
`trigger_parameters.github_app` instead. Projects still on older webhook
triggers return an empty `trigger_parameters` entirely; they are kept on
the board rather than filtered out, with branch shown as `?`.

## Endpoints

| Path | Purpose |
| --- | --- |
| `/` | The radiator. |
| `/api/status` | Current board as JSON. |
| `/healthz` | Liveness. |

## Running it as a background service

    cp deploy/io.willowworks.build-monitor.plist ~/Library/LaunchAgents/
    # edit the paths inside, then:
    launchctl load ~/Library/LaunchAgents/io.willowworks.build-monitor.plist

Logs land in `/tmp/build-monitor.{log,err}`.

## License

MIT. See [LICENSE](LICENSE).
