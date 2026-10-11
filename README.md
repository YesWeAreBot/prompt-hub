<div align="center">

# prompt-hub

**Prompt packs you can version, review and verify — a local renderer for prompts that behaves like a package manager behaves for code.**

`ph` is a daemon plus CLI that installs, dependencies-locks, sandboxes and renders versioned **prompt packs**,
and a registry (`ph hub`) to distribute them.

[![Go Version](https://img.shields.io/badge/Go-1.26.4-00ADD8?logo=go&logoColor=white)](go.mod)
[![Runtimes](https://img.shields.io/badge/runtimes-template%20%C2%B7%20wasm%20%C2%B7%20php-success)](#runtimes)

[简体中文](README.zh-CN.md) · [Quickstart](#quickstart) · [Pack format](#pack-format) · [CLI](#cli-reference) · [HTTP API](#http-api) · [Registry](#registry-ph-hub) · [Security](#security-model)

</div>

---

## What is prompt-hub?

Prompts in production are *software without a build system*: they live in a text field, change without review,
depend on data that nobody declares, and silently drift between environments. `prompt-hub` gives them the
machinery they are missing.

A **pack** is a directory (or a single file, or a `.pack` archive) carrying a manifest, its text or code entry
point, an optional `pack.lock`, and a `fixtures/` directory of golden outputs. `ph` loads packs into a local
**store**, resolves their dependencies, and renders them into the exact text your model receives — under a
sandbox, with an audit log, and with an `ETag` you can cache on.

| Prompt problem | prompt-hub's answer |
| --- | --- |
| "Which prompt version ran in that bad reply?" | Content-addressed **tree hash** for every pack, recorded in a JSONL **ledger** on every render |
| "Two packs forked and drifted apart" | `kind: library` packs + `includes` with `^`/`~` ranges pinned by `pack.lock` |
| "Nobody told us this prompt reads user data" | Every pack declares `requires`; a **profile** classifies each field by stability and privacy |
| "The prompt calls out to the network at render time" | `egress` must be declared *and* allowlisted on the host, otherwise the render is **refused** |
| "Production prompt crashed the bot host" | Per-render **wall timeout, memory cap, process cap**; a failed runtime never silently degrades |
| "Rendering differs between host languages" | One canonical spec, two implementations — Go daemon and the JS SDK are cross-checked byte-for-byte |
| "Shipping a prompt update grants new powers nobody approved" | `ph upgrade` prints a **capability diff** and refuses widening upgrades without `--yes` |
| "Republishing 1.0.0 quietly changed everyone's behaviour" | Same version + different content = **hard rejection**, in both the store and the registry |

### Design rules

1. **Fail loud, never silently degrade.** A missing required context field, an unregistered runtime tier, an
   unauthorized egress host and an unresolved range dependency are all errors. The daemon never renders a
   *slightly different* prompt and calls it success — a silently degraded prompt is a prompt you don't know you got wrong.
2. **The contract is declared, not implied.** `requires` in the manifest is a promise the host must keep. If the
   pack uses a field it didn't declare, `ph check` says so before it ever reaches production.
3. **Content is addressed, not assumed.** Tree hashes are verified on install, on first render, and after download
   from a registry. Mismatch = refusal, never "install anyway".
4. **Capabilities are reviewed when they change.** Upgrades are diffed; a new context field or a new egress target
   is a privilege change and needs explicit consent.

---

## Table of contents

- [Quickstart](#quickstart)
- [Core concepts](#core-concepts)
- [Pack format](#pack-format)
- [Runtimes](#runtimes)
- [Sandbox and limits](#sandbox-and-limits)
- [CLI reference](#cli-reference)
- [HTTP API](#http-api)
- [Registry (`ph hub`)](#registry-ph-hub)
- [Profiles](#profiles)
- [JavaScript SDK](#javascript-sdk)
- [Security model](#security-model)
- [Repository layout](#repository-layout)
- [Development](#development)
- [Examples](#examples)
- [FAQ](#faq)
- [License](#license)

---

## Quickstart

You need **Go 1.26.4+**. The `template` and `wasm` tiers are built in and require nothing else; the `php` tier is optional.

> **A note on the transcripts below.** The daemon and CLI currently emit human-readable messages in Chinese
> (`installed`, `tiers`, `field placement`, … are printed as `已安装`, `档位`, `字段画像`), and the example packs
> in this repository are Chinese-language prompts. For readability, the transcripts below show the same messages
> and sample text in English; run the commands to see the exact strings. Nothing machine-readable is localised:
> pack ids, versions, tree hashes, header names, error `code`s and JSON field names are identical either way.

```console
$ git clone https://github.com/YesWeAreBot/prompt-hub.git
$ cd prompt-hub
$ go build -o bin/ph ./cmd/ph
```

Keep state out of your checkout with `PH_HOME` (defaults to `~/.prompt-hub`):

```console
$ export PH_HOME="$PWD/.ph"        # PowerShell: $env:PH_HOME="$PWD\.ph"
```

### 1. Inspect and test a pack

```console
$ ./bin/ph check examples/nekochan
packId: nekochan
version: 1.0.0
profile: chat/1
kind: content
entry: index.prompt
runtime: template
requires:
    - chat.user.nickname
    - chat.channel.name
outputMax: 4000
license: MIT
---
tree hash: sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
files:     3
manifest:  valid
placement: frozen-session, max privacy internal
  chat.user.nickname           session      internal
  chat.channel.name            session      internal
  → depends on session fields; freeze per session (recompute when the session changes)

$ ./bin/ph test examples/nekochan
PASS  basic render

1/1 passed
```

`ph check` validates the manifest, prints its canonical form, walks the asset closure and classifies every
declared field against its profile. `ph test` replays `fixtures/*.json` and compares the rendered body
**byte for byte**.

### 2. Install and render

```console
$ ./bin/ph install examples/cats-role
installed cats-role@1.2.0
  tree hash  sha256-8c4b0f69c860996fc27572ada8632e7b568ecddd63b229eab358622d4caf77ed
  dir        ~/.prompt-hub/store/cats-role/1.2.0/sha256-8c4b0f69c…

$ ./bin/ph install examples/nekochan-asset
installed nekochan-asset@1.2.0
  tree hash  sha256-e8ea3bb70fde5f3d10e288a9272ad22426611b5edd86b4ab659a765a0ec917a3
  dir        ~/.prompt-hub/store/nekochan-asset/1.2.0/sha256-e8ea3bb70fde5f3d…

$ ./bin/ph list
cats-role@1.2.0
nekochan@1.0.0
nekochan-asset@1.2.0
```

Start the daemon and render through it — this is the path a host application uses:

```console
$ ./bin/ph serve
prompt-hub daemon  listening on http://127.0.0.1:18400
  store     /home/you/.prompt-hub/store
  ledger    /home/you/.prompt-hub/ledger.jsonl
  token     /home/you/.prompt-hub/token
  views     /home/you/.prompt-hub/views
  tiers     template, wasm, php(not enabled)
  sandbox   timeout=5s  memory=256MB  procs=8
  egress    fully disabled (packs declaring egress are refused)
  packs     3 installed

$ ./bin/ph render nekochan@1.0.0 --ctx chat.user.nickname=Bunny --ctx chat.channel.name=debate-team --turn ev_1
HTTP 200 OK
ETag:           "sha256:ebb6f57f5611156d3448ef36ebfeb0f94cb852c73efb452bca99a257eb2d4eb0"
X-Pack-Revs:    nekochan@1.0.0
X-Tree-Hash:    sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
X-Render-Ms:    2
---
You are Huli, the tsundere cat-girl assistant, currently active in "debate-team". You end your
sentences with "nya", act aloof, but are soft-hearted underneath.

[User preferences]
- Address them as: Bunny

[Tone]
- When the room is busy, keep replies short and don't flood the chat.
```

Ask for a field you didn't provide and the daemon tells you exactly what's missing instead of guessing:

```console
$ ./bin/ph render nekochan@1.0.0
HTTP 422 Unprocessable Entity
---
{"code":"missing-context","error":"pack declares required context fields that were not provided: chat.channel.name, chat.user.nickname","partial":false,"retryable":false,"missing":["chat.channel.name","chat.user.nickname"]}
```

### 3. Distribute it

```console
$ ./bin/ph hub serve --addr 127.0.0.1:18500 --publish-token s3cret &
$ ./bin/ph publish examples/nekochan --hub http://127.0.0.1:18500 --token s3cret
published nekochan@1.0.0
  tree hash  sha256-481522e231f8f753137f51026aaf95868a8fe859d12e09dcd0ed8bc3c92c8115
  size       676 bytes
  declares   reads chat.user.nickname, chat.channel.name

$ ./bin/ph index --hub http://127.0.0.1:18500
cats-role@1.2.0            template chat/1       0.7KB
nekochan@1.0.0             template chat/1       0.7KB  reads 2 fields

$ ./bin/ph install nekochan@1.0.0 --hub http://127.0.0.1:18500
$ ./bin/ph upgrade nekochan --hub http://127.0.0.1:18500
nekochan is already up to date (local 1.0.0, hub 1.0.0)
```

To see what a host application does end to end — handshake, `validate`, registering a context callback, and
rendering with live context — run [`examples/host-stub.mjs`](examples/host-stub.mjs) against a running daemon:

```console
$ node examples/host-stub.mjs --token-file ~/.prompt-hub/token --pack nekochan@1.0.0
  app context endpoint http://127.0.0.1:25449/ph/context
  handshake ok: prompt-hub/0.1.0, agreed pull-context, validate
  tiers: template, wasm, php❌
  hint: ph runtime enable php (downloads a portable PHP into $PH_HOME/tools/php)
  validate: needs chat.user.nickname, chat.channel.name; deps (none); tier available yes
  [host] the daemon asked for: chat.channel.name
  render 200  source=store  tree=sha256-481522e231f8f75…
---- rendered ----
You are Huli, the tsundere cat-girl assistant, currently active in "debate-team". …
```

The stub answers the daemon's context callback with a small in-memory session (channel name, nickname,
affinity), and also passes `chat.user.nickname` inline in the query string — which is why the log shows only
`chat.channel.name` being fetched over the callback. Inline values win over fetched ones.

---

## Core concepts

| Term | Meaning |
| --- | --- |
| **Pack** | The installable unit. A directory with `pack.yaml`, a single file whose header comment *is* the manifest, or a `.pack` archive. Identified as `packId@version`. |
| **Manifest** | `pack.yaml` — identity, runtime tier, entry point, and the declared capability surface: `requires`, `egress`, `includes`. |
| **Tree hash** | `sha256-…` over every file in the pack. Content address of a pack *and* of every dependency version. |
| **Store** | Local content-addressed installation root: `<store>/<packId>/<version>/<treeHash>/`. |
| **Resolver** | Finds a pack in the store first, then in read-only **embed layers** (`serve --embed`). |
| **Library pack** | `kind: library` — ships no text itself, only reusable assets/functions, and is the only kind that may be `include`d. |
| **Range / Lock** | `includes: cats-role@^1.2` is a range; `pack.lock` pins the exact version **and its tree hash**. Removing ambiguity is the point. |
| **Profile** | A named schema for host context (`chat/1`): each field carries `stability` and `privacy`. |
| **Placement** | What the profile implies for prompt caching: `frozen-global`, `frozen-session`, or `append-only`. |
| **Runtime tier** | The engine that produces text: `template`, `wasm` (built in), or `php` (optional). |
| **Ledger** | Append-only JSONL audit trail: one line per render attempt, including failures. |

---

## Pack format

### Directory layout

```
nekochan-asset/
├── pack.yaml             # manifest (required)
├── pack.lock             # generated by `ph lock`; pins ranged dependencies
├── index.prompt          # entry point
├── partials/
│   └── intro.md
└── fixtures/
    └── basic.json        # golden fixture: context in, expected text out
```

### `pack.yaml`

```yaml
packId: nekochan-asset          # required; /^[a-z0-9][a-z0-9._-]*$/
version: 1.2.0                  # required; semver, optional -prerelease
profile: chat/1                 # default: chat/1
kind: content                   # content | library         (default: content)
entry: index.prompt             # required for content
runtime: template               # template | wasm | php     (default: template)
includes:                       # dependencies; ranges only via ^ and ~
  - cats-role@^1.2
requires:                       # context fields this pack reads
  - chat.user.nickname
  - chat.channel.name
egress:                         # hosts the runtime may reach (default: none)
  - api.example.com
exports:                        # required for kind: library
  - cats_role
outputMax: 4000                 # output byte cap            (default: 8000)
targets: []                     # free-form labels
license: MIT
```

Validation rules enforced by `pack.Validate`:

- `packId` matches `^[a-z0-9][a-z0-9._-]*$`; `version` matches `^<major>.<minor>.<patch>[-prerelease]?$`.
- `kind: content` **must** declare `entry`; `kind: library` **must not** declare `entry` and **must** declare `exports`.
- `runtime` matches `^[a-z][a-z0-9-]*$`.
- Every `includes` entry parses as `<id>@<exact version | ^range | ~range>`; there are no other range operators.

Ranges accept only `^` and `~`, and exact versions are required when no lock entry exists:

```console
$ ./bin/ph lock examples/nekochan-asset          # pin the ranges, write pack.lock
  cats-role      1.2.0      sha256-8c4b0f69c860996fc27572ada8632e7b568ecddd63b229eab358622d4caf77ed（range ^1.2）
wrote examples/nekochan-asset/pack.lock (1 entry)

$ ./bin/ph lock examples/nekochan-asset --update # move ranges to the highest available version
```

> **Why a lock file at all?** Ranges make authoring convenient and production unpredictable.
> `prompt-hub` resolves ranges at `ph lock` time, records the **tree hash** — not just the version — and the
> render path only ever consumes exact versions. A range without a lock entry is refused at install, check
> and publish time.

### Single-file packs

A prompt so small it doesn't deserve a directory can carry its manifest in a header comment. The hash is
computed over a canonicalised virtual tree, so a single-file pack and the equivalent directory pack agree:

```text
/* packId: single-demo
 * version: 2.0.0
 * runtime: template
 * requires: chat.user.nickname
 */
Hello {{chat.user.nickname}}, this turn is {{turn}}.
```

```console
$ ./bin/ph install ./neko.prompt
installed single-demo@2.0.0
  tree hash  sha256-05f3a48dca1257b75460fa208c32a16d15a6f3f8899faa3f160e510857a86ccd
  dir        ~/.prompt-hub/store/single-demo/2.0.0/sha256-05f3a48dca1257b75460fa208c32a16d15a6f3f8899faa3f160e510857a86ccd
  form       single file (manifest canonicalised from the header comment into pack.yaml)
```

Header keys are normalised (`Pack-Id`/`id`/`name` → `packId`, `dependencies` → `includes`, `context` → `requires`, …).
`entry` must be `self` or the file's own name. `<?php` at the top of the file is tolerated, so a single `.php`
pack works the same way.

### `.pack` archives

The registry moves packs as deterministic gzipped tar:

- paths sorted, `0644` modes, `mtime` pinned to the Unix epoch, uid/gid zeroed, PAX format → **identical bytes for identical content**;
- traversal (`..`), absolute paths, NUL bytes, duplicate entries, non-regular file types, mismatched sizes and entries over 64 MiB are rejected on read.

### Tree hash

The content address of a pack is reproducible across languages:

```
for each path p in byte-order sort(paths):
    fileHash = sha256(content[p])
    h.update(p); h.update("\x000644\x00"); h.update(hex(fileHash) + "\n")
treeHash = "sha256-" + hex(sha256_final(h))
```

Path sorting is **UTF-8 byte order**, not host-collation order, so the same pack hashes identically on every
platform and in the [JavaScript SDK](#javascript-sdk) — verified by cross-implementation tests over ASCII,
CJK and supplementary-plane file names ([`interop_test.go`](internal/render/interop_test.go)).

---

## Runtimes

`pack.yaml`'s `runtime` selects the tier. Tiers are resolved by name at render time; an unavailable tier is a
**hard error with an actionable hint**, never a fallback to a different tier.

```console
$ ./bin/ph runtime
tier         status
  template  ✅ built in (plain text + {{field}})
  wasm      ✅ built in (wazero + WASI sandbox)
  php       ❌ php not found (put it in ~/.prompt-hub/tools/php, or add it to PATH)
            ph runtime enable php (downloads a portable PHP into $PH_HOME/tools/php)
```

### `template` — built in

Plain text with `{{field}}` substitution, plus asset inlining.

**Built-in variables** (never declared in `requires`):

| Variable | Value |
| --- | --- |
| `{{packId}}`, `{{version}}` | Pack identity |
| `{{turn}}` | The turn identifier passed by the host |
| `{{now}}` | Current UTC time, `YYYY-MM-DD HH:MM:SS` |
| `{{nowUnix}}` | Current Unix timestamp |

**`{{asset '<ref>'}}`** inlines another text file, recursively:

- `{{asset './partials/intro.md'}}` — a file from this pack;
- `{{asset 'cats-role/tone.md'}}` — a file from an *included* pack. The pack must be declared in `includes`, otherwise the render fails.

Limits: 256 KiB per asset, 8 levels of nesting, 1 MiB total after expansion. Cycles are reported with the full
chain (`a → b → a`). Binary assets are refused.

**Unknown variables are errors**, reported with the asset they came from:

```
template referenced a variable that was never provided: chat.user.nickname（from cats-role/tone.md）
```

### `wasm` — built in

`entry` is a `.wasm` module compiled for WASI (`GOOS=wasip1 GOARCH=wasm`). The daemon runs it on
[wazero](https://github.com/tetratelabs/wazero):

- the pack view is mounted **read-only at `/`** (`WithReadOnlyDirMount`);
- memory is capped by the render limits, the wall-clock timeout interrupts the module, and compiled modules
  are cached per `treeHash` — `ph serve` **prewarms** them at startup so the first request doesn't pay compile cost;
- stdout is the prompt; stderr becomes a render warning.

```console
$ cd examples/nekochan-wasm && GOOS=wasip1 GOARCH=wasm go build -o index.wasm . && cd ../..
$ ./bin/ph install examples/nekochan-wasm && ./bin/ph render nekochan-wasm@1.0.0 \
    --ctx chat.user.nickname=Bunny --ctx chat.channel.name=debate-team --ctx chat.user.affinity=86
```

### `php` — optional

`entry` is a `.php` file; the daemon calls the interpreter with `-n` (no `php.ini`) and injects
[`_ph/sdk.php`](internal/render/sdk/php/sdk.php), which decodes the render envelope from stdin:

```php
<?php
require __DIR__ . '/_ph/sdk.php';

$nickname = ph_required('chat.user.nickname');
$channel  = ph_required('chat.channel.name');

echo "You are Huli, the tsundere cat-girl assistant, active in \"{$channel}\".\n";
echo "- Address them as: {$nickname}\n";
```

| Helper | Purpose |
| --- | --- |
| `ph($key, $default = null)` | Read a context value |
| `ph_required($key)` | Read or exit non-zero (a missing field is a contract violation) |
| `ph_turn()`, `ph_now()`, `ph_pack()`, `ph_output_max()`, `ph_view()`, `ph_env()` | Envelope metadata |

Included library packs are materialised at `vendor/<packId>/`, so PHP packs compose by ordinary `require`:

```php
require __DIR__ . '/vendor/cats-role/role.php';
```

Install a checksum-verified portable PHP into `$PH_HOME/tools/php` (Windows; on other platforms use your
distribution or `static-php-cli`):

```console
$ ./bin/ph runtime enable php
installing a portable PHP into ~/.prompt-hub/tools/php
reading the release index https://downloads.php.net/~windows/releases/releases.json
selected PHP 8.x.y (nts-vs17-x64, …)
sha256 verified
installed PHP 8.x.y
the php tier is now available: ~/.prompt-hub/tools/php/php.exe
```

---

## Sandbox and limits

Process-based tiers (`php`, and wasm where applicable) are confined per render:

| Limit | Flag | Default |
| --- | --- | --- |
| Wall-clock timeout | `--render-timeout` | `5s` |
| Memory | `--max-render-memory-mb` | `256` (0 = unlimited) |
| Processes | `--max-render-procs` | `8` (0 = unlimited) |
| Output | `outputMax` in the manifest | `8000` bytes |
| Egress | `--allow-egress host1,host2` | none |
| Assets | fixed in code | 256 KiB/file, 8 deep, 1 MiB expanded |

- **Windows**: a Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` plus process/job memory and active-process
  caps; the whole tree dies with the job. If the sandbox cannot be created, the render **fails** unless the
  process was explicitly started unsandboxed.
- **Unix**: a dedicated process group, killed as a group on timeout.
- **Truncation is not silent**: an over-long output still renders, but sets the `X-Ph-Warnings` header, records
  `status: "truncated", degraded: true` in the ledger, and appears in `ph ledger`.
- **Runtime failures never degrade**: a non-zero exit or a timeout is an error response, never a partial prompt.
  Timeouts include the last stderr line and the last stdout line as diagnostics.

---

## CLI reference

```console
$ ./bin/ph --help
ph — local daemon and CLI for prompt packs

  ph serve   [--addr 127.0.0.1:18400] [--store DIR] [--token-file FILE]
             [--php PATH] [--views DIR]
             [--render-timeout 5s] [--max-render-memory-mb 256] [--max-render-procs 8]
             [--allow-egress host1,host2]
  ph install <pack path>            [--store DIR]
  ph check   <pack path>
  ph test    <pack path>
  ph list                           [--store DIR]
  ph render  <id@version> [--ctx k=v]... [--turn ID] [--etag E] [--addr HOST]
  ph hash    <pack path>
  ph ledger  [--n 20] [--ledger FILE]
  …
```

| Command | Purpose |
| --- | --- |
| `ph serve` | Run the local render daemon (default `127.0.0.1:18400`) |
| `ph install <pack \| id@version --hub URL>` | Install from a path, or from a registry with `--no-deps` to skip dependencies |
| `ph check <pack>` | Validate manifest, dependencies, assets and field placement |
| `ph test <pack>` | Replay `fixtures/*.json`; `expect` is compared byte for byte |
| `ph list` | List installed packs |
| `ph render <id@version> [--ctx k=v]… [--turn ID] [--etag E]` | Render through the daemon |
| `ph hash <pack>` | Print the tree hash |
| `ph ledger [--n 20]` | Show the render audit trail |
| `ph lock <pack> [--update]` | Pin ranged dependencies into `pack.lock` |
| `ph remove <id@version>` / `ph gc` | Delete one version / clean incomplete installs |
| `ph export <id@version> --to DIR` | Emit an embed-layer layout for `ph serve --embed` |
| `ph profile [name/version]` | Print the field metadata table |
| `ph runtime [enable php]` | Show tiers / provision PHP |
| `ph hub serve [--root DIR] [--publish-token T]` | Run a registry (default `127.0.0.1:18500`) |
| `ph publish <pack> --hub URL [--token T]` | Publish a pack |
| `ph index --hub URL` | List registry contents with their declared capabilities |
| `ph upgrade <packId> --hub URL [--yes]` | Upgrade, after printing a capability diff |

`ph serve` flags: `--addr`, `--store`, `--token-file`, `--ledger`, `--no-token`, `--php`, `--views`,
`--render-timeout`, `--max-render-memory-mb`, `--max-render-procs`, `--allow-egress`, `--embed`,
`--context-endpoint`, `--context-token`, `--profiles`.

`--no-token` and `--publish-token ""` exist for local experiments only; the daemon warns if you bind anything
other than `127.0.0.1`.

---

## HTTP API

Base URL `http://127.0.0.1:18400`. All routes except `/v1/health` require the token from `$PH_HOME/token`,
sent as `X-Ph-Token: <token>` or `Authorization: Bearer <token>`. Responses are `Cache-Control: no-store`.

| Method & path | Description |
| --- | --- |
| `GET /v1/health` | Liveness plus installed pack count |
| `GET /v1/render?pack=<id@version>&turn=<id>&ctx.<field>=<value>` | Render a pack |
| `GET /v1/validate?pack=<id@version>[&ctx.<field>=<value>…]` | Pre-flight: declared capabilities, dependency resolution, field metadata, placement, and (when context is supplied) which fields are still missing |
| `GET /v1/packs` | Installed packs, including embed layers |
| `GET /v1/packs/{id}/{version}/describe` | Full description of one pack: runtime availability, egress authorisation, dependency closure, profile classification |
| `GET /v1/renderers` | Runtime tiers with availability and enable hints |
| `GET /v1/ledger?n=20` | Recent ledger entries |
| `POST /v1/initialize` | Handshake: register an application name and its context callback endpoint |
| `GET /v1/profiles`, `GET /v1/profiles/{name}/{version}` | Field schemas |

### Render response

Success returns `text/plain; charset=utf-8` with the prompt as the body:

| Header | Meaning |
| --- | --- |
| `ETag` | `"sha256:<hash over treeHash + body>"` — stable for identical content |
| `X-Pack-Revs` | The resolved `id@version` |
| `X-Tree-Hash` | Content address of the pack tree |
| `X-Ph-Source` | `store` or `embed` |
| `X-Render-Ms` | Render time |
| `X-Ph-Warnings` | Truncation and runtime diagnostics, when any |

Send the `ETag` back as `If-None-Match` to receive `304 Not Modified` — the render is still executed, still
recorded in the ledger (`"notModified": true`), but the body is not transferred. This is how a host keeps a
frozen prefix cached across turns.

### Errors

Errors are JSON with a stable machine-readable `code`:

```json
{ "code": "missing-context", "error": "…", "partial": false, "retryable": false,
  "missing": ["chat.channel.name", "chat.user.nickname"] }
```

| Status | `code` | Cause |
| --- | --- | --- |
| 400 | `bad-ref` | `pack` is not `id@version` (ranges are not accepted here) |
| 400 | `not-content` | A `kind: library` pack was asked to render |
| 400 | `app-not-registered` | `X-Ph-App` was sent by an app that never called `/v1/initialize` |
| 401 | `unauthorized` | Missing or wrong token |
| 403 | `origin-rejected` | The request carried an `Origin` header (browsers are refused outright) |
| 403 | `egress-denied` | The pack declares an egress host the host machine has not allowlisted |
| 404 | `not-installed` | Pack or version is absent from store and embed layers |
| 409 | `conflict` | Version conflict on publish |
| 422 | `missing-context` | A declared `requires` field was not supplied (carries `missing[]`) |
| 424 | `dependency` | A dependency could not be resolved |
| 500 | `integrity` | Store contents no longer match their tree hash |
| 502 | `context-callback` | The application's context endpoint failed |

### `POST /v1/initialize`

Registers an application in the daemon's session table (max 64) so that context can be pulled on demand for
*that* application:

```jsonc
// request
{ "protocolVersion": "1", "app": "my-bot",
  "contextEndpoint": "http://127.0.0.1:9000/ph/context", "contextToken": "stub-secret",
  "capabilities": ["pull-context", "validate"] }

// response
{ "protocolVersion": "1", "daemon": "prompt-hub/0.1.0", "app": "my-bot",
  "agreed": ["pull-context", "validate"],
  "capabilities": ["pull-context", "validate", "describe", "dependencies", "embed-layer", "exact-version-pin"],
  "tiers": [ { "name": "template", "available": true }, … ],
  "contextConfigured": true }
```

The daemon then `POST`s `{"fields": […]}` to the registered loopback endpoint and expects
`{"values": { … }}`. Constraints, deliberately narrow:

- the callback must be **loopback** `http`/`https`;
- the registration is keyed by `X-Ph-App`, and there is **no fallback to a default source** — a request from an
  unregistered app fails with `app-not-registered` rather than mixing one application's context into another's prompt;
- values supplied inline in the query string win over fetched values;
- callback responses are capped at 1 MiB and 2 seconds.

---

## Registry (`ph hub`)

The registry is a file-backed registry: `packs/<id>/<version>.json` metadata plus deduplicated
`blobs/<treeHash>.pack` archives.

| Method & path | Description |
| --- | --- |
| `GET /v1/health` | Liveness and published-version count |
| `GET /v1/index` | All published versions with manifests and live `missingDeps` |
| `GET /v1/packs/{id}/{version}` | Metadata for one version |
| `GET /v1/blobs/{treeHash}` | The archive |
| `POST /v1/packs` | Publish (requires `X-Ph-Publish-Token` when configured; 128 MiB body cap) |

Publish-time policy:

- ranged dependencies **must** be accompanied by an up-to-date `pack.lock`, otherwise the publish is rejected;
- **only library packs may be depended on** — depending on a `content` pack is rejected;
- any referenced pack that isn't published yet is reported as `missingDeps` and surfaced by `ph index` as a
  warning instead of blocking the publish;
- re-publishing a version with different content is a `409` — publish a new version;
- clients verify the downloaded archive's tree hash against the metadata and refuse to install on mismatch.

`ph upgrade` never silently widens capabilities. It diffs runtime, profile, `outputMax`, `requires` and `egress`
and refuses to proceed without `--yes` when the diff widens:

```
nekochan@1.0.0 -> nekochan@1.1.0
  runtime    template -> php
  output cap 4000 -> 8000
  added context fields +chat.user.affinity
  added egress targets +api.example.com
error: this upgrade **widens capabilities** (see above); pass --yes to accept it explicitly
```

---

## Profiles

Context fields are not strings with names; they are typed, classified data. A profile declares each field's
`stability` and `privacy`:

| `stability` | Meaning |
| --- | --- |
| `static` | Identical for all users and turns — safe in a globally frozen prompt prefix |
| `session` | Fixed for the lifetime of a session, recomputed when the session changes |
| `per-request` | Changes every turn — must go into an append-only tail |

| `privacy` | Meaning |
| --- | --- |
| `public` | No personal data |
| `internal` | Internal identifiers |
| `pii` | Personal data — must be visible to the user before it goes to a remote model |

`Classify` folds a pack's `requires` into a **placement** and a **maximum privacy**:

| Placement | Derived from | Consequence |
| --- | --- | --- |
| `frozen-global` | only `static` fields | can live in a globally frozen prefix |
| `frozen-session` | some `session` fields | frozen per session; recomputed when the session changes |
| `append-only` | any `per-request` field, or an unknown field | must be appended every turn |

Unknown fields are reported, never ignored — in `ph check`, in `ph serve` logs, and in the
`/v1/packs/{id}/{version}/describe` response. The built-in `chat/1` profile (15 fields) lives in
[`internal/profile/chat@1.yaml`](internal/profile/chat@1.yaml); extra profiles load from
`<profiles>/<name>/<version>.yaml` and override built-ins with the same name.

```console
$ ./bin/ph profile chat/1
chat/1  Standard context fields for chat-style applications.

field                        type        stability    privacy    description
chat.assistant.id            string      static       internal   the assistant's own identifier; same for everyone, at all times
chat.clock.now               timestamp   per-request  public     current time
…
```

> `ph profile` currently prints the profile's own `description` text verbatim, so with the built-in profile the
> heading and per-field descriptions come out in Chinese.

---

## JavaScript SDK

[`sdk/js/packhub.mjs`](sdk/js/packhub.mjs) is a dependency-free implementation of the pack format for hosts that
run in Node.js (or in an edge worker, via the same algorithms): manifest parsing, tree hashing, `.pack` reading,
asset closure, and `template` rendering.

```js
import { loadPack, renderTemplate } from './sdk/js/packhub.mjs';

const pack = loadPack('examples/nekochan');
const { body, warnings } = renderTemplate(pack, {
  'chat.user.nickname': 'Bunny',
  'chat.channel.name': 'debate-team',
}, { turn: 'ev_1', now: new Date('2026-10-10T13:30:00Z') });

console.log(body);
```

Exported surface: `loadPack`, `loadSingleFile`, `loadArchive`, `isArchivePath`, `readTar`, `treeHash`,
`renderTemplate`, `parseManifestYaml`, `parseHeaderComment`, `normalizeManifest`, `canonicalManifestJson`,
`assetClosure`, `assetRefs`, `checkAssets`, `undeclaredAssetVars`, `parseAssetArg`, `isBuiltinVar`,
`truncateUtf8`, `formatNow`.

There is a companion CLI with no dependencies:

```console
$ node sdk/js/cli.mjs hash   examples/nekochan-asset
$ node sdk/js/cli.mjs check  examples/nekochan
$ node sdk/js/cli.mjs render examples/nekochan-asset --turn ev_1 \
    --ctx chat.user.nickname=Bunny --ctx chat.channel.name=debate-team --dep cats-role=examples/cats-role
```

The Go daemon and this SDK are held to the **same output**: `internal/render/interop_test.go` renders the repo's
fixtures with both implementations and compares the bodies byte for byte, including packs that inline assets
across a dependency boundary.

---

## Security model

`ph serve` is a **local** daemon and the design assumes it:

- **Loopback by default, browsers refused.** Binding a non-`127.0.0.1` address prints a warning, and any request
  carrying an `Origin` header is rejected with `403` — a page in a browser cannot reach the daemon even if it
  guesses the port.
- **Token required.** A 32-character token is generated on first run at `$PH_HOME/token` (`0600`) and is required
  on every route except `/v1/health`.
- **Egress is deny-by-default and two-sided.** A pack must declare `egress` *and* the host must allowlist it via
  `--allow-egress`; the renderer additionally receives `PH_EGRESS_ALLOW` so a runtime can enforce it itself.
- **Context callbacks stay on loopback.** Registered context endpoints must resolve to a loopback host, and each
  app's registration is isolated from every other app's.
- **Renders are bounded.** Timeout, memory, process count, output size and asset expansion all have caps; the
  sandbox failing is an error, not a warning, unless it was explicitly disabled.
- **Content is verified at every hop.** Install, first render, dependency resolution and registry download all
  recompute the tree hash, and mismatch is fatal.
- **Archives are treated as hostile.** Path traversal, absolute paths, duplicate entries, malformed tar headers
  and oversized entries are rejected while reading, before anything touches the filesystem.
- **The store is immutable per version.** `<packId>/<version>` may contain exactly one tree hash; installing
  different content under a version that already exists is refused.

**Not in scope.** `prompt-hub` does not call a model, does not store conversation data (only hashes and
identifiers in the ledger), and does not pretend a token-guarded loopback daemon is a multi-tenant service. The
`php` tier executes pack code — that code is untrusted by construction, which is what the sandbox is for.

---

## Repository layout

```
cmd/ph/                 CLI + daemon entry point (all subcommands)
internal/
  pack/                 manifest, version ranges, pack.lock, tree hashing, archives
  store/                content-addressed store, embed layers, dependency closure
  render/               runtime tiers, asset expansion, process sandbox
    sdk/php/sdk.php     PHP SDK injected into every PHP render
  httpapi/              daemon routes, context callbacks, app sessions, describe/validate
  hub/                  registry server and client, capability diffs
  profile/              profile schema, classification, built-in chat/1
  ledger/               append-only JSONL audit log
  phpfetch/             checksum-verified portable PHP provisioning
examples/               seven worked packs + a host application stub
sdk/js/                 dependency-free JavaScript SDK and its test suite
```

---

## Development

```console
$ go build ./...                 # build everything
$ go vet ./...                   # vet
$ go test ./...                  # full suite (includes cross-language fixtures)
$ node --test sdk/js/test.mjs    # JavaScript SDK suite
```

The Go suite covers manifest validation, version-range semantics, lock resolution, archive hardening, store
conflicts and garbage collection, resolver precedence, profile classification, HTTP guards and error codes,
the registry, and the sandbox (including compiling and running a real WASI module). Tests that need an optional
toolchain (`node` for the cross-language fixtures, `php` for the PHP tier) skip themselves rather than fail, so a
clean checkout is green everywhere.

Two notes for a clean working tree: the WASI test rebuilds
[`internal/render/testdata/wasmstub/wasmstub.wasm`](internal/render/testdata/wasmstub/main.go) in place, and
several tests create scratch directories under `internal/render/` (`ph-wasm-*`, `ph-uni-*`, `ph-single-*`) —
all of which are covered by `.gitignore` and cleaned up by the tests themselves.

No CI is wired up yet: `go test ./...` plus `node --test sdk/js/test.mjs` is the entire test story, and both are
meant to be run locally before a change lands.

Ground rules for changes:

1. **Keep the two implementations identical.** Any change to hashing, manifests or template semantics must be
   accompanied by a JS SDK change and an interop test update.
2. **Errors are part of the API.** New failure modes need a stable `code`, a status, and — ideally — an
   actionable hint in the message.
3. **Never add a silent fallback.** If a declared capability cannot be honoured, fail.
4. **Fixtures are the spec.** When behaviour changes, `examples/*/fixtures/*.json` change with it.

---

## Examples

| Pack | Tier | Demonstrates |
| --- | --- | --- |
| [`nekochan`](examples/nekochan) | `template` | Minimal content pack with a golden fixture |
| [`cats-role`](examples/cats-role) | — | `kind: library`, reusable persona assets + a PHP export |
| [`nekochan-asset`](examples/nekochan-asset) | `template` | `{{asset}}` across a dependency boundary, recursive expansion, `pack.lock` |
| [`live-data`](examples/live-data) | `template` | Per-request context (`chat.clock.now`, `chat.turn.user_text`) and the `append-only` placement it forces |
| [`nekochan-php`](examples/nekochan-php) | `php` | Dynamic prompt built in PHP, two fixtures covering both branches |
| [`nekochan-lib`](examples/nekochan-lib) | `php` | `require`ing an included library pack from `vendor/` |
| [`nekochan-wasm`](examples/nekochan-wasm) | `wasm` | A Go/WASI module as the prompt entry point (compile it first: `GOOS=wasip1 GOARCH=wasm go build -o index.wasm .`) |
| [`host-stub.mjs`](examples/host-stub.mjs) | — | A host application: handshake, `validate`, context callback, render |

---

## FAQ

**Does the daemon call a model?**
No. It produces text. Whatever calls the model stays in your application, which is why every render returns the
tree hash and the ETag of exactly what was produced.

**Do I have to use the daemon?**
No. `ph check`, `ph test`, `ph hash` and `ph lock` are offline tools, and the JavaScript SDK renders `template`
packs with no daemon at all.

**Why is `library` the only includable kind?**
So that composition always has an explicit shape: a library ships no prompt of its own, only parts you inline or
functions you call. A `content` pack that others depend on would make "which prompt actually ran" ambiguous.

**Why refuse unknown template variables?**
Because a typo in `{{chat.user.nickname}}` otherwise produces a prompt with a hole in it, and you find out from
your users.

**Why does `304 Not Modified` still count as a render in the ledger?**
Because a cached prefix is still a decision to send that prompt. The ledger records `notModified: true` so the
audit trail shows that the content was deliberately reused, not merely absent.

**What happens when the `php` tier isn't installed?**
Any pack that declares `runtime: php` fails with an error that includes the exact enable command. `ph test` skips
such packs with the same explanatory message instead of failing.

---

## License

Released under the [MIT License](LICENSE) — © 2026 YesWeAreBot.

Each example pack additionally declares `license: MIT` in its own manifest, which describes the pack's contents
rather than this repository.
