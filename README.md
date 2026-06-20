# allure3-docker-service-go

> ⚠️ **Status: early development (work in progress).** The migration has just begun — there is **no working Go service or published Docker image yet**. The documentation below describes the **target** service: its API and configuration are inherited from the upstream Allure 2 project and adapted for Allure 3. Treat it as the contract we are building toward, not as something already runnable. Commands that reference a published image are aspirational until the first release.

A web service that stores and serves **Allure 3** test reports with the history of previous runs.

This is a **fork** of [`fescobar/allure-docker-service`](https://github.com/fescobar/allure-docker-service), being rewritten from **Python/Flask + Allure 2 (Java)** to **Go + Allure 3 (Node.js)**. See [Differences from upstream](#differences-from-upstream).

Table of contents
=================
* [FEATURES](#features)
   * [Docker Hub](#docker-hub)
   * [Docker Versions](#docker-versions)
      * [Image Variants](#image-variants)
* [USAGE](#usage)
   * [Generate Allure Results](#generate-allure-results)
   * [ALLURE DOCKER SERVICE](#allure-docker-service)
      * [SINGLE PROJECT - LOCAL REPORTS](#single-project---local-reports)
         * [Single Project - Docker on Unix/Mac](#single-project---docker-on-unixmac)
         * [Single Project - Docker on Windows (Git Bash)](#single-project---docker-on-windows-git-bash)
         * [Single Project - Docker Compose](#single-project---docker-compose)
      * [MULTIPLE PROJECTS - REMOTE REPORTS](#multiple-projects---remote-reports)
         * [Multiple Project - Docker on Unix/Mac](#multiple-project---docker-on-unixmac)
         * [Multiple Project - Docker on Windows (Git Bash)](#multiple-project---docker-on-windows-git-bash)
         * [Multiple Project - Docker Compose](#multiple-project---docker-compose)
         * [Creating our first project](#creating-our-first-project)
   * [Single Port (4040 removed)](#single-port-4040-removed)
   * [Known Issues](#known-issues)
   * [Opening & Refreshing Report](#opening--refreshing-report)
   * [User Interface](#user-interface)
   * [Deploy using Kubernetes](#deploy-using-kubernetes)
   * [Extra options](#extra-options)
      * [Allure API](#allure-api)
         * [Info Endpoints](#info-endpoints)
         * [Action Endpoints](#action-endpoints)
         * [Project Endpoints](#project-endpoints)
         * [Security Endpoints](#security-endpoints)
      * [Send results through API](#send-results-through-api)
         * [Content-Type - application/json](#content-type---applicationjson)
         * [Content-Type - multipart/form-data](#content-type---multipartform-data)
         * [Force Project Creation Option](#force-project-creation-option)
      * [Customize Executors Configuration](#customize-executors-configuration)
      * [API Response Less Verbose](#api-response-less-verbose)
      * [Switching port](#switching-port)
      * [Updating seconds to check Allure Results](#updating-seconds-to-check-allure-results)
      * [Keep History and Trends](#keep-history-and-trends)
      * [Override User Container](#override-user-container)
      * [Start in DEV Mode](#start-in-dev-mode)
      * [Enable TLS](#enable-tls)
      * [Enable Security](#enable-security)
         * [Login](#login)
         * [X-CSRF-TOKEN](#x-csrf-token)
         * [Refresh Access Token](#refresh-access-token)
         * [Logout](#logout)
         * [Roles](#roles)
         * [Make Viewer endpoints public](#make-viewer-endpoints-public)
         * [Scripts](#scripts)
      * [Multi-instance Setup](#multi-instance-setup)
      * [Add Custom URL Prefix](#add-custom-url-prefix)
      * [Optimize Storage](#optimize-storage)
      * [Export Native Full Report](#export-native-full-report)
      * [Allure Options](#allure-options)
* [Differences from upstream](#differences-from-upstream)
* [SUPPORT](#support)
* [DEVELOPMENT (Usage for developers)](#development-usage-for-developers)
* [Acknowledgements](#acknowledgements)
* [License](#license)

## FEATURES
Allure Framework provides good-looking reports for test automation. Normally, seeing an up-to-date report means generating and opening it locally after every run — tedious on a shared team setup.

This container turns that into a long-running web server. You mount your `allure-results` directory (Single Project) or your `projects` directory (Multiple Projects), or feed results over the API. Every time new results appear, the service generates a fresh **Allure 3 (Awesome)** report and archives it as the next run — visible by refreshing your browser.

- Useful for developers who run tests locally and want to inspect regressions.
- Useful for a team to track test status per project, with full history of past runs.

The service only **generates reports from results** — you produce the `allure-results` with whatever Allure 3 adapter your stack uses (pytest, TestNG, JUnit, Cucumber, Playwright, etc.).

### Docker Hub
- **Not published yet** (work in progress). Until the first release, [build the image from source](#development-usage-for-developers).

### Docker Versions
The report engine is the [Allure 3](https://allurereport.org/) CLI (Node.js), installed via npm. This is a fundamental change from upstream, which bundled the Allure 2 commandline (Java/JDK).

#### Image Variants
The target images support `amd64`, `arm64` and `armv7`, built on a Node.js runtime base (the Go API binary is statically compiled and copied in).

| **Base Image**           | **Arch** | **OS**  |
|--------------------------|----------|---------|
| node:lts (amd64)         | amd64    | linux   |
| node:lts (arm64)         | arm64    | linux   |
| node:lts (arm/v7)        | armv7    | linux   |

## USAGE
### Generate Allure Results
This service only generates reports **based on results**. You must generate `allure-results` according to your test stack using an Allure 3 adapter.

- Allure 3 docs & adapters: https://allurereport.org/docs/
- Allure integrations: https://github.com/allure-framework

The raw `allure-results` format (the `*-result.json` / `*-container.json` files plus attachments) is what you send to this service.

### ALLURE DOCKER SERVICE

| **Project Type**  | **Port** | **Volume Path**       | **Container Volume Path** |
|-------------------|----------|-----------------------|---------------------------|
| Single Project    | 5050     | `${PWD}/allure-results` | `/app/allure-results`   |
| Multiple Projects | 5050     | `${PWD}/projects`       | `/app/projects`         |

To navigate JSON API responses comfortably, a browser JSON viewer extension helps.

#### SINGLE PROJECT - LOCAL REPORTS
Recommended for local executions. Attach the volume where your project generates results. All local executions are stored under the `default` project, created automatically on start. Inspect it via:

- `http://localhost:5050/allure-docker-service/projects/default`

##### Single Project - Docker on Unix/Mac
```sh
docker run -p 5050:5050 -e CHECK_RESULTS_EVERY_SECONDS=3 -e KEEP_HISTORY=1 \
           -v ${PWD}/allure-results:/app/allure-results \
           allure3-docker-service-go
```

##### Single Project - Docker on Windows (Git Bash)
```sh
docker run -p 5050:5050 -e CHECK_RESULTS_EVERY_SECONDS=3 -e KEEP_HISTORY=1 \
           -v "/$(pwd)/allure-results:/app/allure-results" \
           allure3-docker-service-go
```

##### Single Project - Docker Compose
```yaml
services:
  allure:
    image: "allure3-docker-service-go"   # not published yet — build from source
    environment:
      CHECK_RESULTS_EVERY_SECONDS: 1
      KEEP_HISTORY: 1
    ports:
      - "5050:5050"
    volumes:
      - ${PWD}/allure-results:/app/allure-results
```

```sh
docker compose up allure          # add -d to run in background
docker compose logs -f allure
```

NOTE:
- `${PWD}/allure-results` can live anywhere on your machine; your project must write results there.
- `/app/allure-results` is the path **inside** the container — do not change it, or change detection will break.
- On Windows `${PWD}` only works in [Git Bash](https://git-scm.com/downloads); in PowerShell/CMD use the full path.

#### MULTIPLE PROJECTS - REMOTE REPORTS
Generate reports for multiple isolated projects. Create/delete/list them via the [Project Endpoints](#project-endpoints) (Swagger documents them).

IMPORTANT:
- For multiple projects use `CHECK_RESULTS_EVERY_SECONDS=NONE`. Otherwise a watcher polls every project's `results` directory and regenerates on any change, which is costly in CPU/memory/storage. Instead generate on demand with `GET /generate-report` after `POST /send-results`.

##### Multiple Project - Docker on Unix/Mac
```sh
docker run -p 5050:5050 -e CHECK_RESULTS_EVERY_SECONDS=NONE -e KEEP_HISTORY=1 \
           -v ${PWD}/projects:/app/projects \
           allure3-docker-service-go
```

##### Multiple Project - Docker on Windows (Git Bash)
```sh
docker run -p 5050:5050 -e CHECK_RESULTS_EVERY_SECONDS=NONE -e KEEP_HISTORY=1 \
           -v "/$(pwd)/projects:/app/projects" \
           allure3-docker-service-go
```

##### Multiple Project - Docker Compose
```yaml
services:
  allure:
    image: "allure3-docker-service-go"   # not published yet — build from source
    environment:
      CHECK_RESULTS_EVERY_SECONDS: NONE
      KEEP_HISTORY: 1
      KEEP_HISTORY_LATEST: 25
    ports:
      - "5050:5050"
    volumes:
      - ${PWD}/projects:/app/projects
```

NOTE:
- `/app/projects` is the path inside the container — do not change it, or project data won't persist.

##### Creating our first project
Create the project `my-project-id` via `POST /projects`:
```sh
curl -X POST http://localhost:5050/allure-docker-service/projects \
  -H 'Content-Type: application/json' \
  -d '{ "id": "my-project-id" }'
```

- List projects: `GET /projects`
- The `default` project is always created automatically and must not be removed.
- Inspect a project: `GET /projects/{id}`

To work with a specific project, pass the `project_id` query parameter on the [Action Endpoints](#action-endpoints). For example the latest report:

- Default project: `.../allure-docker-service/latest-report` → `.../projects/default/reports/latest/index.html?redirect=false`
- A named project: `.../allure-docker-service/latest-report?project_id=my-project-id` → `.../projects/my-project-id/reports/latest/index.html?redirect=false`

```
GET  /latest-report?project_id=my-project-id
POST /send-results?project_id=my-project-id
GET  /generate-report?project_id=my-project-id
GET  /clean-results?project_id=my-project-id
GET  /clean-history?project_id=my-project-id
GET  /report/export?project_id=my-project-id
```

On-disk project structure:
```
projects
  |-- default
  |   |-- results
  |   |-- reports
  |   |   |-- latest
  |   |   |-- 3
  |   |   |-- 2
  |   |   |-- 1
  |-- my-project-id
  |   |-- results
  |   |-- reports
  |   |   |-- latest
  |   |   |-- ...
```

NOTE:
- Do not modify a project's directory structure manually.
- Mount the volume at `/app/projects`, otherwise project data is lost.

### Single Port (4040 removed)
Upstream historically exposed port `4040` for the Allure report and `5050` for the API. This fork **serves everything on port `5050` only**; the deprecated `4040` single-report server (`allure open`) has been removed. Render the latest report at:

- `http://localhost:5050/allure-docker-service/latest-report`

### Known Issues
- **Allure 3 history bootstrap** — early Allure 3 versions may not emit the history directory on the first run, affecting Status Dynamics / trends ([allure3#455](https://github.com/allure-framework/allure3/issues/455)). The service is expected to bootstrap history explicitly; pin a known-good Allure 3 version.
- `Permission denied` on mounted volumes — usually a user/permission mismatch; see [Override User Container](#override-user-container).

### Opening & Refreshing Report
On a healthy run you will see logs similar to:
```
Checking Allure Results every 1 second/s
Creating executor.json for PROJECT_ID: default
Generating report for PROJECT_ID: default
Report successfully generated to /app/.../projects/default/reports/latest
Detecting results changes for PROJECT_ID: default
Storing report history for PROJECT_ID: default
BUILD_ORDER: 1
```

Open the latest report at:
- `http://localhost:5050/allure-docker-service/latest-report`

It redirects to the report resource:
- `http://localhost:5050/allure-docker-service/projects/default/reports/latest/index.html?redirect=false`

The `latest` report is regenerated automatically (Single Project) and may be briefly unavailable while a new one builds — you'll see a `NOT FOUND` page for a few seconds. The `redirect=false` parameter avoids being redirected to the `GET /projects/{id}` page when the report isn't ready.

Run more tests without touching the server: new results produce a new report and accumulate in the history/trend widgets — just refresh the browser.

### User Interface
The **Allure 3 Awesome report is the UI** — it is self-sufficient and served directly by this service. The separate Angular UI container that upstream used (`allure-docker-service-ui`) has been **removed** in this fork; it is no longer needed.

### Deploy using Kubernetes
The service is a stateless web server plus a mounted volume for `projects`/`allure-results`, so it deploys like any container: a Deployment + Service, with a PersistentVolume for the data directory. Use `CHECK_RESULTS_EVERY_SECONDS=NONE` and drive generation via the API.

### Extra options

#### Allure API
All endpoints are served under the base prefix `/allure-docker-service/` (configurable, see [Add Custom URL Prefix](#add-custom-url-prefix)). Swagger UI with live examples is served at the service root.

##### Info Endpoints
```
GET /version
GET /config
GET /swagger
GET /swagger.json
```

##### Action Endpoints
```
GET  /latest-report
POST /send-results        (admin role)
GET  /generate-report     (admin role)
GET  /clean-results       (admin role)
GET  /clean-history       (admin role)
GET  /report/export
```

##### Project Endpoints
```
POST   /projects              (admin role)
GET    /projects
DELETE /projects/{id}         (admin role)
GET    /projects/{id}
GET    /projects/{id}/reports/{path}
GET    /projects/search
```

##### Security Endpoints
```
POST   /login
POST   /refresh
DELETE /logout
DELETE /logout-refresh-token
```
To access Security Endpoints you must [Enable Security](#enable-security).

#### Send results through API
After your tests run, push the generated `allure-results` to the server with `POST /send-results`. Two content types are supported.

##### Content-Type - application/json
Each file is sent as `{ file_name, content_base64 }`.
- Python: [`allure-docker-api-usage/send_results.py`](allure-docker-api-usage/send_results.py)
- Python (security): [`allure-docker-api-usage/send_results_security.py`](allure-docker-api-usage/send_results_security.py)
- Jenkins declarative pipeline: [`send_results_jenkins_pipeline.groovy`](allure-docker-api-usage/send_results_jenkins_pipeline.groovy)
- Jenkins (security): [`send_results_security_jenkins_pipeline.groovy`](allure-docker-api-usage/send_results_security_jenkins_pipeline.groovy)
- PowerShell: [`send_results.ps1`](allure-docker-api-usage/send_results.ps1)

##### Content-Type - multipart/form-data
Files are sent as `files[]`.
- Bash: [`allure-docker-api-usage/send_results.sh`](allure-docker-api-usage/send_results.sh)
- Bash (security): [`allure-docker-api-usage/send_results_security.sh`](allure-docker-api-usage/send_results_security.sh)

These examples send the sample results in [`allure-docker-api-usage/allure-results-example`](allure-docker-api-usage/allure-results-example). To wipe results use `GET /clean-results`.

##### Force Project Creation Option
Pass `force_project_creation=true` to auto-create a missing project:
```
POST /send-results?project_id=any-unexistent-project&force_project_creation=true
```

#### Customize Executors Configuration
`GET /generate-report` accepts query params that populate the report's "Executor" widget:
- `execution_name` — label of the run.
- `execution_from` — URL back to the CI job, e.g. `GET /generate-report?execution_from=http://my-jenkins/job/my-job/7/`
- `execution_type` — icon, e.g. `jenkins` (unknown types fall back to the default icon).

#### API Response Less Verbose
Enable `API_RESPONSE_LESS_VERBOSE` when handling large numbers of files, to avoid transferring big file listings. The JSON response shape changes.
```yaml
    environment:
      API_RESPONSE_LESS_VERBOSE: 1
```

#### Switching port
The API listens on `5050` inside the container. Remap as needed:
```yaml
    ports:
      - "9292:5050"
```

#### Updating seconds to check Allure Results
Controls how often the `results` directory is polled to regenerate reports automatically.
```yaml
    environment:
      CHECK_RESULTS_EVERY_SECONDS: 5
```
Use `NONE` to disable automatic checking — then reports are only built via `GET /generate-report`:
```yaml
    environment:
      CHECK_RESULTS_EVERY_SECONDS: NONE
```

- **Enabled** (`=3`) is best for a **local** machine: any change in `allure-results` triggers a new report. Because it regenerates on existing files too, the run count can look inflated — fine locally.
- **`NONE`** is best on a **server** fed by CI: nothing regenerates until you call the API. Recommended workflow per execution:

```
--- EXECUTION 1 ---
1. GET  /clean-results     # drop results from previous executions
2. run your test suites
3. POST /send-results      # upload this execution's results
4. GET  /generate-report   # build the report (archived as a new run)
--- EXECUTION 2 ---
repeat (always clean results first)
```

Cleaning first ensures a report represents exactly one execution.

#### Keep History and Trends
Enable `KEEP_HISTORY` to accumulate history & trends across runs:
```yaml
    environment:
      KEEP_HISTORY: 1
      KEEP_HISTORY_LATEST: 20
```
Each run is archived under a numbered directory (`latest` mirrors the last one). In Allure 3 the trend history is backed by `history.jsonl` rather than the Allure 2 history-folder copy. By default the latest `20` builds are retained — raise or lower with `KEEP_HISTORY_LATEST`. Reset history with `GET /clean-history`.

#### Override User Container
Run as a non-root user that can write the mounted volumes (`1000:1000` is the `allure` user):
```yaml
    user: 1000:1000
```
or pass the current user:
```sh
MY_USER=$(id -u):$(id -g) docker compose up -d allure
```
Avoid running containers as `root`.

#### Start in DEV Mode
Verbose request logging for debugging (not for production):
```yaml
    environment:
      DEV_MODE: 1
```

#### Enable TLS
Serve over `https`; cookies are then marked `Secure`:
```yaml
    environment:
      TLS: 1
```

#### Enable Security
If you expose this API publicly, you **MUST** combine security with [Enable TLS](#enable-tls), otherwise tokens/cookies can be intercepted.

Define the admin user and enable security:
```yaml
    environment:
      SECURITY_ENABLED: 1
      TLS: 1
      SECURITY_USER: "my_username"
      SECURITY_PASS: "my_password"
```
`SECURITY_PASS` is case-sensitive.

##### Login
`POST /login` issues access + refresh tokens as cookies (plus their CSRF cookies):
```sh
curl -X POST http://localhost:5050/allure-docker-service/login \
  -H 'Content-Type: application/json' \
  -d '{ "username": "my_username", "password": "my_password" }' \
  -c cookiesFile -ik
```

##### X-CSRF-TOKEN
Mutating requests use double-submit CSRF: send the CSRF cookie value back as the `X-CSRF-TOKEN` header.
```sh
CSRF_ACCESS_TOKEN_VALUE=$(cat cookiesFile | grep -o 'csrf_access_token.*' | cut -f2)
curl -X POST http://localhost:5050/allure-docker-service/projects \
  -H "X-CSRF-TOKEN: $CSRF_ACCESS_TOKEN_VALUE" -H 'Content-Type: application/json' \
  -d '{ "id": "my-project-id" }' -b cookiesFile -ik
```

##### Refresh Access Token
When the access token expires, refresh it instead of logging in again, using the refresh cookie + its CSRF header:
```sh
CSRF_REFRESH_TOKEN_VALUE=$(cat cookiesFile | grep -o 'csrf_refresh_token.*' | cut -f2)
curl -X POST http://localhost:5050/allure-docker-service/refresh \
  -H "X-CSRF-TOKEN: $CSRF_REFRESH_TOKEN_VALUE" -c cookiesFile -b cookiesFile -ik
```
The access token expires in 15 minutes by default; tune with `ACCESS_TOKEN_EXPIRES_IN_MINS` (or `ACCESS_TOKEN_EXPIRES_IN_SECONDS` for dev). The refresh token never expires by default; tune with `REFRESH_TOKEN_EXPIRES_IN_DAYS` (or `_SECONDS`). Use `0` to disable expiry.

##### Logout
- `DELETE /logout` invalidates the current access token (send the `csrf_access_token` as `X-CSRF-TOKEN`).
- `DELETE /logout-refresh-token` invalidates the refresh token and clears all cookies (send the `csrf_refresh_token`).

##### Roles
`SECURITY_USER` / `SECURITY_PASS` define the **admin** (full access). Optionally add a read-only **viewer**:
```yaml
    environment:
      SECURITY_USER: "my_username"
      SECURITY_PASS: "my_password"
      SECURITY_VIEWER_USER: "view_user"
      SECURITY_VIEWER_PASS: "view_pass"
      SECURITY_ENABLED: 1
```
- The admin user must always be defined.
- Admin and viewer usernames must differ.
- Admin-only endpoints are marked *(admin role)* in [Allure API](#allure-api).

##### Make Viewer endpoints public
Protect only admin endpoints and leave read/viewer endpoints public:
```yaml
    environment:
      SECURITY_ENABLED: 1
      MAKE_VIEWER_ENDPOINTS_PUBLIC: 1
```
With this enabled, a defined `viewer` user has no effect.

##### Scripts
Secured client examples:
- [`send_results_security.sh`](allure-docker-api-usage/send_results_security.sh)
- [`send_results_security.py`](allure-docker-api-usage/send_results_security.py)
- [`send_results_security_jenkins_pipeline.groovy`](allure-docker-api-usage/send_results_security_jenkins_pipeline.groovy)

#### Multi-instance Setup
If you run multiple instances behind a load balancer, set `JWT_SECRET_KEY` (the same value on every instance), otherwise requests may fail with `Invalid Token - Signature verification failed`. (Token revocation/blacklist is also expected to use a shared store across instances.)

#### Add Custom URL Prefix
Mount the API behind a reverse-proxy path with `URL_PREFIX`:
```yaml
    environment:
      URL_PREFIX: "/my-prefix"
```
```sh
curl http://localhost:5050/my-prefix/allure-docker-service/version
```
Example nginx config where `allure` is the container name:
```nginx
location /my-prefix/ {
    proxy_pass http://allure:5050;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Host $server_name;
}
```
Not supported when `DEV_MODE` is enabled.

#### Optimize Storage
Allure writes some heavy static assets into every report that never change between builds. With `OPTIMIZE_STORAGE` enabled, those are consumed from a shared in-container location instead of being duplicated per report, reducing storage drastically.
```yaml
    environment:
      OPTIMIZE_STORAGE: 1
```
NOTE:
- Which assets are deduplicated depends on the report format; this is being revisited for the Allure 3 Awesome layout.
- Reports generated with different Allure versions may not be guaranteed to render if shared assets change.

#### Export Native Full Report
Download the full native report as a zip via `GET /report/export` (per project with `?project_id=`).

#### Allure Options
Allure 3 is configured via an `allurerc.mjs` file (or a static `.json`/`.yml`), where you select the report plugin (this fork defaults to **Awesome**), history settings, report name, etc. This replaces the Allure 2 `ALLURE_OPTS` JVM flags used by upstream. Link patterns for TMS/issue trackers are configured in your test adapter and shipped inside `allure-results`. See https://allurereport.org/docs/.

## Differences from upstream
This fork targets **Allure 3 only**, with no backward compatibility with Allure 2.

| Layer | Upstream (`fescobar/allure-docker-service`) | This fork |
|---|---|---|
| API | Python / Flask | **Go** (chi) |
| Report engine | Allure 2 CLI (Java / JDK) | **Allure 3** CLI (Node.js) |
| Report format | Allure 2 | Allure 3 **Awesome** |
| Orchestration | bash scripts | native Go |

Removed:
- Separate Angular UI container — the Awesome report is the UI.
- Emailable report (`/emailable-report/*`) — was tied to the Allure 2 data layout.
- Deprecated port `4040` (`allure open`).
- Legacy duplicate "bare" routes — endpoints live under a single `/allure-docker-service/` prefix.

## SUPPORT
This is an early-stage fork. Please open issues and questions on this repository's tracker. For upstream (Allure 2) behaviour and history, see [`fescobar/allure-docker-service`](https://github.com/fescobar/allure-docker-service).

## DEVELOPMENT (Usage for developers)
Until an image is published, build and run locally.

```sh
# Build the image from source
docker build -f docker/Dockerfile -t allure3-docker-service-go .

# Or use the dev compose file
docker compose -f docker-compose-dev.yml up
```

> The build is mid-migration: the repository still contains the original Python/Flask service while the Go rewrite is in progress. Expect the Dockerfile, compose file and CI to change as the migration lands.

## Acknowledgements
Huge thanks to **Frank Escobar** ([@fescobar](https://github.com/fescobar)) and the contributors of [`allure-docker-service`](https://github.com/fescobar/allure-docker-service) — the original Allure 2 project this fork is based on. This migration would not exist without their work.

Allure Report is a project of [Qameta Software](https://allurereport.org/) and the [`allure-framework`](https://github.com/allure-framework) community.

## License
[Apache License 2.0](LICENSE)
