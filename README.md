# allure3-docker-service-go

A web service that stores and serves **Allure 3** test reports with the history of previous runs.

This is a **fork** of [`fescobar/allure-docker-service`](https://github.com/fescobar/allure-docker-service), rewritten from **Python/Flask + Allure 2 (Java)** to **Go + Allure 3 (Node.js)**. See [Differences from upstream](#differences-from-upstream).

> ⚠️ **Status: 0.0.1, no authentication.** Everything documented below works, but the service authenticates nobody: whoever can reach the port can upload results and delete projects. Deploy it **only inside a trusted internal network**. Built-in auth is planned for 0.0.2 — see [Not implemented yet](#not-implemented-yet).

Table of contents
=================
* [What it does](#what-it-does)
* [Quick start](#quick-start)
   * [Docker Compose](#docker-compose)
   * [Docker run](#docker-run)
   * [Running from source](#running-from-source)
* [Generating Allure results](#generating-allure-results)
* [Configuration](#configuration)
* [Storage layout](#storage-layout)
* [HTTP API](#http-api)
   * [Info endpoints](#info-endpoints)
   * [Project endpoints](#project-endpoints)
   * [Results endpoints](#results-endpoints)
   * [Report generation](#report-generation)
   * [Report endpoints](#report-endpoints)
* [Typical CI workflow](#typical-ci-workflow)
* [History and trends](#history-and-trends)
* [Opening the report](#opening-the-report)
* [Deploying](#deploying)
   * [File permissions](#file-permissions)
   * [Kubernetes](#kubernetes)
* [Known issues](#known-issues)
* [Not implemented yet](#not-implemented-yet)
* [Differences from upstream](#differences-from-upstream)
* [Development](#development)
* [Acknowledgements](#acknowledgements)
* [License](#license)

## What it does

Allure Framework produces good-looking reports for test automation. Normally, seeing an up-to-date report means generating and opening it locally after every run — tedious on a shared team setup.

This container turns that into a long-running web server. Your CI uploads the `allure-results` of a run over the API, the service generates a fresh **Allure 3 (Awesome)** report and publishes it at a stable URL, archiving the previous run so trends accumulate across executions.

- Useful for a team to track test status per project, with the history of past runs.
- Useful for developers who run tests locally and want to inspect regressions.

The service only **generates reports from results** — you produce the `allure-results` with whatever Allure adapter your stack uses (pytest, TestNG, JUnit, Cucumber, Playwright, etc.).

Multiple isolated projects are supported out of the box; a project called `default` is always created on start.

## Quick start

No image is published yet — build from source. Everything is served on a single port, **5050**.

### Docker Compose

The repository ships a ready [`docker-compose.yml`](docker-compose.yml):

```sh
mkdir -p .data/projects && sudo chown -R 1000:1000 .data/projects   # Linux only, see File permissions
docker compose up -d --build
docker compose logs -f allure-service
```

```yaml
services:
  allure-service:
    build:
      context: .
      dockerfile: docker/Dockerfile
      args:
        ALLURE_VERSION: 3.15.0
    image: allure3-service:0.0.1
    restart: unless-stopped
    environment:
      KEEP_HISTORY: 1
      KEEP_HISTORY_LATEST: 60
      CHECK_RESULTS_EVERY_SECONDS: 0     # 0 — watcher off, reports are built on request
    ports:
      - "${ALLURE_SERVICE_PORT:-5050}:5050"
    volumes:
      - ./.data/projects:/app/projects
```

Mount the **projects root as a whole**. Publishing a report renames a directory out of `.tmp` into `reports/latest`, and `rename` does not work across filesystems — mounting a subdirectory of it breaks generation.

### Docker run

```sh
docker build -f docker/Dockerfile -t allure3-service:0.0.1 .

docker run -d -p 5050:5050 \
           -e KEEP_HISTORY=1 -e KEEP_HISTORY_LATEST=60 \
           -v ${PWD}/.data/projects:/app/projects \
           allure3-service:0.0.1
```

On Windows `${PWD}` only works in [Git Bash](https://git-scm.com/downloads) (`-v "/$(pwd)/.data/projects:/app/projects"`); in PowerShell/CMD use an absolute path.

### Running from source

Go 1.26 and the [Allure 3](https://allurereport.org/docs/) CLI on `PATH` are required:

```sh
STATIC_CONTENT_PROJECTS="$PWD/.local/projects" go run ./cmd/allure-service
```

`STATIC_CONTENT_PROJECTS` is mandatory on a dev machine: the default `/app/projects` is a container path and `MkdirAll` on it fails with `permission denied`. The directory (and the `default` project inside it) is created on start.

The Allure CLI is resolved at startup with `exec.LookPath`; if it is missing, or `allure --version` fails, the service exits immediately rather than discovering it on the first build:

```
2026/08/13 00:34:39 history limit 60
2026/08/13 00:34:39 allure /opt/homebrew/bin/allure (3.15.0)
2026/08/13 00:34:39 Starting server on port 5050
```

`Ctrl+C` / `SIGTERM` shuts down gracefully (5-second drain).

## Generating Allure results

This service generates reports **from results** — you must produce `allure-results` yourself with an Allure adapter for your test stack.

- Allure docs & adapters: https://allurereport.org/docs/
- Allure integrations: https://github.com/allure-framework

The raw `allure-results` directory (the `*-result.json` / `*-container.json` files plus attachments) is what you upload to the service.

## Configuration

All configuration is environment variables; invalid values fall back to the default with a warning in the log.

| Variable | Default | Effect |
|---|---|---|
| `PORT` | `5050` | HTTP listen port |
| `STATIC_CONTENT_PROJECTS` | `/app/projects` | Projects root on disk. Must be set when running outside the container |
| `ALLURE_BIN` | `allure` | Allure CLI name or path; a bare name is looked up in `PATH` |
| `KEEP_HISTORY` | `true` | Accumulate run history between builds. `false` means **erase**: the history limit collapses to `0` and `history.jsonl` is truncated on every build |
| `KEEP_HISTORY_LATEST` | `60` | How many past runs to keep — the same number of points in the trend chart, and the same number of archived reports |
| `CHECK_RESULTS_EVERY_SECONDS` | `0` | Watcher interval. `0` disables it; reports are then built only via the API |

The effective history limit is printed at startup as `history limit N`.

`SECURITY_ENABLED=1` **refuses to start** (`SECURITY_ENABLED is not supported`) — better a loud failure than a service that silently ignores the flag and serves everything unauthenticated. `OPTIMIZE_STORAGE`, `TLS` and `DEV_MODE` are parsed but do nothing yet.

### The watcher

With `CHECK_RESULTS_EVERY_SECONDS=N` the service polls every project's `results/` directory every `N` seconds and starts a build whenever its fingerprint (file count, total size, newest mtime) changes. The first sweep only records fingerprints, so a restart does not rebuild everything.

- **On** (e.g. `3`) suits a **local** machine, where you drop results into the mount and want a report without calling anything.
- **Off** (`0`) suits a **server fed by CI**: nothing regenerates until the pipeline asks for it, and a report then corresponds to exactly one execution. This is the default and what [`docker-compose.yml`](docker-compose.yml) ships with.

## Storage layout

```
projects
  |-- default
  |   |-- results              # uploaded allure-results
  |   |-- reports
  |   |   |-- latest           # the published report
  |   |   |-- 3                # archived runs
  |   |   |-- 2
  |   |   |-- 1
  |   |-- history.jsonl        # trend history
  |   |-- .tmp                 # builds in progress
  |-- my-project-id
  |   |-- ...
```

Reports are published by **build-then-swap**: the CLI writes into `.tmp/build-*`, and the finished report replaces `reports/latest` with a single `rename`. A failed, timed-out or killed build therefore leaves the published report untouched.

Do not modify a project's directory structure by hand.

## HTTP API

Base URL in the examples is `http://localhost:5050`. There is no `/allure-docker-service` prefix — this fork serves a flat, resource-oriented API. No endpoint requires authentication in 0.0.1.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health` | Liveness probe |
| `GET` | `/config` | Settings the service actually runs with |
| `GET` | `/version` | Allure CLI version |
| `GET` | `/projects` | List projects (optional `?search=`) |
| `POST` | `/projects` | Create a project |
| `GET` | `/projects/{id}` | List a project's builds |
| `DELETE` | `/projects/{id}` | Delete a project |
| `POST` | `/projects/{id}/results` | Upload results files |
| `DELETE` | `/projects/{id}/results` | Wipe uploaded results |
| `POST` | `/projects/{id}/generation` | Start a report build (async) |
| `GET` | `/projects/{id}/generation` | State of the last build |
| `POST` | `/projects/{id}/history/clean` | Reset trends and rebuild |
| `GET` | `/projects/{id}/latest-report` | Redirect to the published report |
| `GET` | `/projects/{id}/reports/{path...}` | Serve report files |
| `GET` | `/projects/{id}/report/export` | Download the report as a zip |

Request and response examples for each group follow below.

### Info endpoints

```sh
curl -s http://localhost:5050/health
# service is ok

curl -s http://localhost:5050/config
# {"keep_history":true,"keep_history_latest":60,"check_results_every_seconds":0}

curl -s http://localhost:5050/version
# {"version":"3.15.0"}
```

`/config` reports the subset of settings that actually influence behaviour; `/version` is read from the binary itself (`allure --version`) at startup, not from a build-time file, so it cannot drift from what is installed.

### Project endpoints

```sh
# create
curl -i -X POST http://localhost:5050/projects \
  -H 'Content-Type: application/json' -d '{"project_id": "my-project"}'
# 201 Created

# list
curl -s http://localhost:5050/projects
# {"projects":["default","my-project"]}

curl -s "http://localhost:5050/projects?search=my"
# {"projects":["my-project"]}

# builds of a project — newest first, "latest" always leading
curl -s http://localhost:5050/projects/default
# {"builds":["latest","3","2","1"]}

# delete
curl -i -X DELETE http://localhost:5050/projects/my-project
# 204 No Content
```

A `project_id` may contain lowercase letters, digits, spaces, `_` and `-`, must start and end with a letter or digit, and is limited to 200 characters. The `default` project cannot be deleted (`403`).

### Results endpoints

Upload with `multipart/form-data`, repeating the `files[]` field:

```sh
curl -i -X POST http://localhost:5050/projects/default/results \
  -F 'files[]=@./allure-results/9f0a-result.json' \
  -F 'files[]=@./allure-results/environment.properties'
```

```
HTTP/1.1 200 OK
{"processed_files":["9f0a-result.json","environment.properties"],"processed_files_count":2}
```

Each filename is sanitised: the path is dropped (`a/b/x.json` → `x.json`) and only ASCII letters, digits, `.`, `_` and `-` survive. Empty files are skipped silently. The total upload is capped at 1 GB. Uploads are not transactional — files written before an error stay on disk, and a retry overwrites them, since Allure names results after UUIDs.

Wipe the results (top-level files only; the published report is untouched):

```sh
curl -i -X DELETE http://localhost:5050/projects/default/results
# 204 No Content
```

### Report generation

Generation is asynchronous. `POST` starts a build and returns immediately; `GET` on the same URL reports how it went.

```sh
curl -i -X POST http://localhost:5050/projects/default/generation
# 202 Accepted

curl -s http://localhost:5050/projects/default/generation
```

```json
{
  "state": "succeeded",
  "started_at": "2026-08-13T00:34:42.809611+03:00",
  "finished_at": "2026-08-13T00:34:43.060984+03:00"
}
```

`state` is `running`, `succeeded` or `failed`; a `failed` build adds an `error` field carrying the CLI's stderr. **A failed build is still HTTP 200** — reading the status succeeded, only the build failed.

Two notable refusals, both `409`:

- **a build of this project is already running.** The running build may have started *before* your results were uploaded, so it is not silently reused. Poll until the state leaves `running`, then `POST` again.
- **the results directory is empty.** Allure would happily build an empty report and publishing it would erase the last good one.

The status registry lives **in memory only**: after a restart it is empty, so a project with a report on disk still answers `404` here.

Reset the trend history — deletes the numbered archives, `history.jsonl` and `executor.json`, then immediately starts a fresh build:

```sh
curl -i -X POST http://localhost:5050/projects/default/history/clean
# 202 Accepted
```

### Report endpoints

```sh
# stable bookmarkable URL → 302 to reports/latest/
curl -i http://localhost:5050/projects/default/latest-report

# the report itself
curl -i http://localhost:5050/projects/default/reports/latest/

# an archived run
curl -i http://localhost:5050/projects/default/reports/3/

# the whole report as a zip, streamed, everything under <id>-report/
curl -f -o report.zip http://localhost:5050/projects/default/report/export
unzip -t report.zip | tail -2
```

The redirect is `302`, not `301`: the target depends on what is on disk, and a permanent redirect would stick in browser caches with no way to recall it.

Export streams the archive as it walks the report, holding the project's build lock so it cannot splice together two different reports. Once the first byte is on the wire the status is fixed at `200` — a mid-stream failure yields a truncated zip and a line in the log, which is why `unzip -t` afterwards is worth it.

## Typical CI workflow

With the watcher off, one execution is one report:

```sh
BASE=http://localhost:5050/projects/default

# 1. drop the previous execution's results
curl -sf -X DELETE $BASE/results

# 2. run your tests, producing ./allure-results

# 3. upload this execution's results
curl -sf -X POST $BASE/results $(printf -- '-F files[]=@%s ' ./allure-results/*)

# 4. build the report and wait for the outcome
curl -sf -X POST $BASE/generation
until [ "$(curl -sf $BASE/generation | jq -r .state)" != "running" ]; do sleep 2; done
curl -sf $BASE/generation | jq
```

Cleaning first is what makes a report represent exactly one execution. If the project may not exist yet, `POST /projects` first and ignore the `409`.

## History and trends

With `KEEP_HISTORY` enabled, every build appends a line to `<project>/history.jsonl` and archives the report under a numbered directory, so the next report can draw the "Status dynamics" trend widget — one bar per past run plus the current one.

Bars of past runs are **clickable**: a click opens that run (`reports/{N}/`) in a new tab. No configuration needed — the service injects an Allure plugin that stamps each history entry with the address of its archive. Two caveats:

- links only exist for runs built by a service version that has the plugin; older history lines have no address and their bars stay inert;
- `KEEP_HISTORY_LATEST` trims archives and history together, so a link disappears along with its trend point rather than rotting into a 404.

`POST /projects/{id}/history/clean` starts the history over.

## Opening the report

The stable entry point per project:

- `http://localhost:5050/projects/default/latest-report`

which redirects to the report resource:

- `http://localhost:5050/projects/default/reports/latest/`

Because publishing is an atomic rename, `latest` never shows a half-written report: until a build finishes, the previous report is still served. Run more tests, upload, generate — then just refresh the browser.

## Deploying

### File permissions

The container runs as **UID 1000** (`node`). On Linux the host directory behind the mount must be owned by it, otherwise the service cannot create projects:

```sh
mkdir -p .data/projects && sudo chown -R 1000:1000 .data/projects
```

Docker Desktop on macOS and Windows handles ownership for you. Do not work around this by running the container as `root`.

### Kubernetes

The service is a single stateless process plus a data directory, so it deploys like any container: a Deployment, a Service on port 5050, and a PersistentVolumeClaim mounted at `/app/projects` (`ReadWriteOnce` is enough — do not run several replicas over the same volume, the build locks are per process). Keep `CHECK_RESULTS_EVERY_SECONDS=0` and drive generation from CI. `GET /health` works as both liveness and readiness probe; the image already declares an equivalent `HEALTHCHECK`.

## Known issues

- **Allure 3 history bootstrap** — some Allure 3 versions do not emit history on the very first run, which affects Status Dynamics / trends ([allure3#455](https://github.com/allure-framework/allure3/issues/455)). The image pins a known-good version via the `ALLURE_VERSION` build arg; change it deliberately.
- **`Permission denied` on the mounted volume** — a UID mismatch, see [File permissions](#file-permissions).
- **Generation status is lost on restart** — it is in-memory by design; the reports themselves are on disk and unaffected.

## Not implemented yet

Parsed or planned, but with no behaviour behind them today:

- **Authentication** (`SECURITY_ENABLED`, JWT login/refresh/logout, admin & viewer roles) — planned for 0.0.2. Until then, keep the service on a trusted network.
- **TLS** (`TLS`) — terminate it at a reverse proxy for now.
- **`OPTIMIZE_STORAGE`**, **`DEV_MODE`** — parsed, ignored.
- **Swagger / OpenAPI document** — the endpoint table and examples above are the API reference for now.
- **`URL_PREFIX`** — mount the service at the proxy root for now. Report links are relative, so a path-stripping proxy works.
- **Emailable report**, **published Docker image**, **CI pipeline**.

## Differences from upstream

This fork targets **Allure 3 only**, with no backward compatibility with Allure 2.

| Layer | Upstream (`fescobar/allure-docker-service`) | This fork |
|---|---|---|
| API | Python / Flask | **Go**, stdlib `net/http` (`ServeMux`), one dependency (`google/uuid`) |
| Report engine | Allure 2 CLI (Java / JDK) | **Allure 3** CLI (Node.js) |
| Report format | Allure 2 | Allure 3 **Awesome** |
| Orchestration | bash scripts | native Go |
| Base image | JDK + Python | `node:24-slim`, static Go binary, single process, UID 1000 |
| API shape | `/allure-docker-service/*` with `?project_id=` | flat REST under `/projects/{id}/...` |
| Generation | synchronous `GET /generate-report` | asynchronous `POST`/`GET .../generation` |

Removed:

- The separate Angular UI container — the Awesome report is the UI.
- The emailable report (`/emailable-report/*`) — tied to the Allure 2 data layout.
- The deprecated port `4040` (`allure open`) — everything is on `5050`.
- Legacy duplicate "bare" routes and the `project_id` query parameter — the project is part of the path.
- The single-project `/app/allure-results` mount — `default` is an ordinary project under `/app/projects`.

## Development

```sh
go build ./...
go vet ./...
gofmt -l internal/ cmd/     # prints files needing formatting; silence is clean
go test ./...
go test -race ./...         # internal/report is concurrent
```

Package layout, with the dependency direction enforced by convention:

```
cmd/allure-service → internal/httpapi → internal/report → internal/projects
                   → internal/watcher
                   → internal/config
```

- **`internal/projects`** owns the on-disk contract: directory layout, project-ID validation, filename sanitisation. Everything else asks it for paths instead of joining them.
- **`internal/report`** is the generation engine: per-project locks, the in-memory status registry, build-then-swap publishing.
- **`internal/watcher`** polls results directories and starts builds when they change.
- **`internal/httpapi`** is the stdlib router plus hand-written middleware (`recoverer(requestID(logger(mux)))`).
- **`internal/config`** reads the environment — only `main` uses it, so nothing else needs `os.Setenv` in tests.

Issues and questions belong on this repository's tracker. For upstream (Allure 2) behaviour, see [`fescobar/allure-docker-service`](https://github.com/fescobar/allure-docker-service).

## Acknowledgements

Huge thanks to **Frank Escobar** ([@fescobar](https://github.com/fescobar)) and the contributors of [`allure-docker-service`](https://github.com/fescobar/allure-docker-service) — the original Allure 2 project this fork is based on. This migration would not exist without their work.

Allure Report is a project of [Qameta Software](https://allurereport.org/) and the [`allure-framework`](https://github.com/allure-framework) community.

## License

[Apache License 2.0](LICENSE)
