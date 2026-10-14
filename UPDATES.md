> **Status:** IN PROGRESS · **Updated:** 2026-10-14 (board test passed)

# Over-the-air updates

The encoder updates its own software from **GitHub Releases** of `enhanced-irl/eirl-encoder`. Releases are
signed with **ed25519**. The policy:

- **Check automatically, notify, install only on a button press**: on the touch screen, in the web, or
  from EnhancedIRL once the cloud supports it.
- **Never while streaming.**
- **Roll back automatically** if the new version does not come up healthy.

**Only our software is updated**: the app, the engine, the helpers, their units and configuration
files. **Never L4T, the kernel, the bootloader or Ubuntu packages.**

| Piece | Code |
|---|---|
| Versioning, manifest, signature, download, the app's state machine | `manager/internal/update` |
| Root helper: verify again, back up, install, health check, roll back | `manager/internal/update/installer`, `manager/cmd/eirl-update`, `manager/deploy/eirl-update{,-recover}.service` |
| API `/update` | `manager/internal/api/update.go`; wiring in `manager/cmd/eirl-encoderd/update.go` |
| Package and bundle | `manager/build.sh`, `manager/deploy/install.sh` (`--list-files`) |
| Release tool | `tools/release.sh`, `manager/cmd/eirl-release` |
| Release key and notes | `release/eirl-release.pub`, `release/notes/<version>/{en,es,fr}.md` |

## 1. How an update flows

```
GitHub Releases (manifest.json + .sig + bundle)
   │  every 6 h ± 30 min (2 min after start), only online; never downloads while streaming
   ▼
eirl-encoderd (user eirl-encoder, sandboxed)
   checks the signature, version and minFrom; downloads to /var/lib/eirl-encoder/update and verifies sha256
   → state "ready" → the user presses "Update" → POST /update/install
   │  writes request.json; sudo -n /bin/systemctl --no-block start eirl-update.service (exact sudoers rule)
   ▼
eirl-update.service (root oneshot, NOT a child of the app)
   copies the files into its own directory and verifies them AGAIN, refuses while streaming,
   backs up every path of the old and the new files.txt, runs the bundle's install.sh,
   restarts the services, health check (90 s) → installed | rolled_back
   ▼
/var/lib/eirl-update/status.json → the app shows the result
```

## 2. Versions

- **One version for the whole bundle**: semver `X.Y.Z`, tag `vX.Y.Z`; the first release is `v0.1.0`.
  The source of truth is the **`VERSION` file** at the repo root: bump it to make a release.
  `tools/release.sh` refuses a version whose tag already exists. A dev build (`manager/build.sh`
  without `EIRL_VERSION`) is `0.0.0-dev+<commit>[.dirty]`, which sorts below every release.
- It is stamped into every binary (`-X main.version=`): app, splash, netsetup, helper and Go engine.
  The board keeps:
  - `/usr/lib/eirl-encoder/VERSION`: one line, the release;
  - `/usr/lib/eirl-encoder/BUILD`: `commit=<sha> date=<RFC3339>`;
  - `/usr/lib/eirl-encoder/files.txt`: every path the release installed, and the retired engine files it
    removed (so that a rollback brings those back too).

  `install.sh` installs these three files **last**, so a VERSION file only names a release once
  all of its files are in place.
- The engine's own `/usr/lib/eirl-encoder/engine/VERSION` stays. `tools/deploy-engine.sh` writes it as
  free text; a bundle writes `<release> <commit> release · <date>`. Tools read its first field.
- **API**: `GET /status` gains `version: {release, app, engine, commit}` and `update: {state, available,
  canInstall}` (additive); `GET /update` is in §8.

## 3. Trust model

**What is trusted:** one ed25519 public key, **compiled into the binaries**
(`internal/update.ProductionKey`, also in `release/eirl-release.pub`, and a test checks the two are
equal). Nothing on the network is trusted: not GitHub, not TLS, not the storage host GitHub redirects
to. The signature covers the **exact bytes** of `manifest.json`; the manifest pins the bundle's size
and sha256.

**Who can trigger what:**

| Actor | Can | Cannot |
|---|---|---|
| Anyone on the network (MITM, a fake release server) | Withhold updates, or slow them down | Make a board install anything: no valid signature |
| The web or the screen (session, or the local socket) | Check now, install the **verified** downloaded release (not while streaming) | Choose what is installed, pass paths or arguments |
| The app process, if compromised | Start `eirl-update.service`, write files in its own update directory | Get root through it: the helper takes only **data** from the app and re-verifies everything with the compiled-in key (below) |
| Root | Anything (`eirl-update rollback` for a manual downgrade) | — |

**Why the helper re-verifies.** The app is unprivileged and sandboxed, so it might be compromised.
The helper treats the app's directory as hostile input:
1. It opens each file with `O_NOFOLLOW` and requires a regular file owned by the app's user, within
   size limits.
2. It **copies** the files into its own root-only directory (`/var/lib/eirl-update/staging`, 0700).
3. It verifies **the copies**, so the app cannot swap a file after the check: signature with the
   compiled-in key, product, the request's version, size and sha256 of the bundle, no downgrade and
   `minFrom`.
4. It extracts the bundle safely: regular files and directories only, no absolute or `..` names,
   modes reduced to 0755/0644, size and count limits. It checks that the bundle's `VERSION` is the
   manifest's.
5. It validates `files.txt` against an allow-list of directories:
   - `/usr/local/{bin,sbin}`, `/usr/lib/eirl-encoder`, `/usr/share/doc/eirl-encoder`;
   - `/etc/systemd/system`, `/etc/sudoers.d`, the polkit directory, `/etc/tmpfiles.d`, the udev
     rules, the NetworkManager dispatcher;
   - `/boot/extlinux/extlinux.conf` (the splash entry).

   `/etc/eirl-encoder` is never touched (MANAGER.md §5).

**Downgrade and replay.** A release is installed only if it is **strictly newer** than the installed
one. An old signed manifest replayed by an attacker therefore does nothing: it is not newer. The only
downgrade is `sudo eirl-update rollback`, by the operator, to the backup kept on the board.

**`minFrom`** is the oldest installed version allowed to update directly to a release, for releases
that need an intermediate step. A board below it shows `min_from` and has to be reinstalled by hand.
Dev builds (`0.0.0-dev+…`) and installs from before the OTA (no VERSION) count as below any `minFrom`.

**Key custody.**
- The private key is a 32-byte seed in base64. It lives only in the maintainers' secret store;
  `EIRL_RELEASE_KEY_CMD` is the command that prints it for `tools/release.sh`.
- `tools/release.sh` reads it **only through a pipe** into `eirl-release sign`. It is never written to
  disk, never in argv or the environment, never printed and never put in an error message.
- The signing tool zeroes the decoded seed and the private key after signing. It cannot zero every
  copy: the line read from stdin (a Go string and bufio's buffer) stays in memory until the process
  exits, a moment later.
- `eirl-release sign` refuses a seed that does not match the expected public key, so a release can
  never be signed with the wrong key.

**Test builds.** `tools/release.sh build --local … --key-stdin --pubkey <test key>` links a different
key into the binaries (`-X …/internal/update.trustedKey=`). Such a build trusts **only** that key, and
it says so:
- the app and the helper log `TEST BUILD`;
- `GET /update` returns `testBuild: true`.

The test key can only get onto a board by installing that build by hand (root). To leave it,
reinstall a normal build.

**Key rotation (plan, not built yet).**
1. Ship a release that trusts **both** the old and the new key (`TrustedKeys` returns a list), signed
   with the old key.
2. Once the fleet has it, sign with the new key.
3. Later, remove the old key.

If the key leaks, a release signed with the old key must carry the new key alone. Boards that never
take that release keep trusting the leaked key; there is no revocation without a channel we control.

## 4. Release assets and the bundle

Three assets per GitHub release:

```jsonc
// manifest.json (pretty-printed; the signature covers its exact bytes)
{ "v": 1, "product": "eirl-encoder", "version": "0.2.0", "released": "2026-10-20T10:00:00Z",
  "minFrom": "0.1.0",                                       // optional
  "notes": { "en": "…", "es": "…", "fr": "…" },             // en required
  "bundle": { "name": "eirl-encoder-0.2.0-linux-arm64.tar.gz", "size": 12345678, "sha256": "…" } }
```

- `manifest.json.sig` is the base64 ed25519 signature of the exact bytes of `manifest.json`.
- `eirl-encoder-<version>-linux-arm64.tar.gz` is the bundle. Its name must be exactly that: the
  board rejects any other name.
- The board fetches the default URL
  `https://github.com/enhanced-irl/eirl-encoder/releases/latest/download/manifest.json`. The
  signature and the bundle come from the directory of the last redirect that stays on that host
  (`…/releases/download/vX.Y.Z/`), so a release published in between cannot mix files from two
  releases.

The bundle is the package of `manager/build.sh` plus the engine, packed deterministically (sorted
entries, root:root, fixed modes):

```
install.sh  deploy/  splash/        what install.sh has always installed
eirl-encoderd  eirl-netsetup  eirl-update  mediamtx  srtla_send
engine/lib/*  engine/units/*        the engine as tools/deploy-engine.sh installs it (its file lists are read
                                    from that script): eirl-engine-go, eirl-engine-start, eirl-enginectl,
                                    eirl-hdmi-init.sh, units
engine/retired.txt                  what the engine no longer uses (deploy-engine.sh's RETIRED: the Python
                                    engine), removed by install.sh
doc/                                licences → /usr/share/doc/eirl-encoder (Go toolchain and modules, MediaMTX,
                                    srtla_send AGPL-3.0 + where its source is)
VERSION  BUILD  files.txt           files.txt = `install.sh --list-files`
```

`install.sh` installs every file through `put()`, which writes a hidden temporary file in the same
directory and renames it. So:
- a running binary or script is never overwritten in place;
- a power cut never leaves half a file;
- the temporary name is ignored by sudo, systemd, udev, polkit and the NetworkManager dispatcher.

**Every file `put()` installs must also be listed in `list_files()`**, the function that produces
files.txt: that is what gets backed up. The container test (§9) checks that every listed path exists
after an install. When a bundle carries the engine, `install.sh`:
- creates its system user `eirl-engine` (the same block as `tools/deploy-engine.sh`);
- installs the code;
- removes the retired engine files (`engine/retired.txt`, with their `.bak-*` copies), as `deploy-engine.sh` does;
- installs the units.

It does not restart the engine (the helper does). By hand, `--restart-engine` restarts it unless it
is streaming.

## 5. Release procedure

```bash
# 1. VERSION = the new version; release/notes/<version>/{en,es,fr}.md (user-facing, short); commit
# 2. Build and sign (needs Go, Docker for the engine, and a Vault token that can read the release key)
tools/release.sh build [--min-from X.Y.Z]
#    → build/out/release/<version>/{eirl-encoder-<v>-linux-arm64.tar.gz, manifest.json, manifest.json.sig}
#    It runs the Go tests, builds everything, signs and verifies with the production key.
# 3. Optional: try it on a test board first (§9, with a test build)
# 4. Publish (separate and explicit: asks you to type the version; every board is offered it within ~6 h)
tools/release.sh publish
git tag -a v<version> -m "eirl-encoder <version>" && git push origin v<version>
# 5. Check it the way a board does
go -C manager run ./cmd/eirl-release check -url https://github.com/enhanced-irl/eirl-encoder/releases/latest/download/manifest.json -current <previous>
```

## 6. On the board

| Path | What |
|---|---|
| `/var/lib/eirl-encoder/update/` (app, `StateDirectory`) | `manifest.json`, `manifest.json.sig` and `bundle.tar.gz` (a verified download), `request.json` (what to install), `state.json` (last check) |
| `/var/lib/eirl-update/` (root, 0755) | `status.json` (0644, the helper's last run), `in-progress.json` (marker), `lock` |
| `/var/lib/eirl-update/backup/` (0700) | `backup-<version>.tar`: the release before the installed one |
| `/var/lib/eirl-update/staging/` (0700) | the copies being verified and the extracted bundle (emptied after each run) |

`eirl-encoderd` flags (set them in `/etc/default/eirl-encoderd`, which only root can write):
- `-update-url`: the manifest URL; empty disables updates;
- `-update-dir`;
- `-update-check=false`: no automatic checks (manual ones still work).

Without `/usr/local/sbin/eirl-update` installed, `/update` answers 503.

## 7. The root helper (`eirl-update`)

`eirl-update install` (the `ExecStart` of `eirl-update.service`):
1. Takes a lock. If the marker of an interrupted run is there, it restores that run's backup first
   and refuses this one.
2. **Verifies** (§3). Anything wrong → `refused` with a code; **nothing has changed**.
3. **Streaming check**:
   - it reads the engine's live state `/run/eirl-engine/mixer.json` (`output.mode = on`, or
     `output.state` connecting/on) and, without it, `OUTPUT=on` in `/run/eirl-engine/state`;
   - both are read in one step, and `install.sh --restart-engine` uses the same rule;
   - streaming → `refused: streaming`.
4. **Backup** of every path in the old `files.txt` and the new one that exists: files and symlinks
   with owner, mode and time, into `backup-<installed>.tar` (fsynced). The paths of the new release
   that do not exist yet are recorded as *absent*, and a restore removes them. On the first OTA from
   an install that predates it, there is no old `files.txt`: the new bundle's paths are backed up.
5. **Marker** `in-progress.json`. From here on, any failure, crash or power cut brings the old
   release back.
6. Runs the bundle's **`install.sh`**:
   - non-interactive, with a fixed PATH, a minimal environment and `EIRL_OTA=1`, and a 10 min timeout;
   - it restarts the app;
   - its output goes to the journal (`journalctl -u eirl-update`).
7. **Engine restart**, only if the engine was running and the bundle carries it:
   - it checks streaming again, a few milliseconds before the restart. Streaming in the meantime (the
     user started it on the screen) → **rollback** `streaming`: all or nothing, never new files under
     an old running engine;
   - the helper never re-owns `/var/lib/eirl-engine` itself: root walking a tree that the engine's user
     can write is a symlink race. systemd's `StateDirectory=` re-owns it (fd-based) when the unit's
     `User=` changes. A hand install with `--restart-engine` uses GNU `chown -R` (fts, does not follow
     links), as `deploy-engine.sh` does.
     The board runs systemd 237-3ubuntu10.57, which has the fix for CVE-2018-15687 (before
     237-3ubuntu10.6 that chown followed symlinks).
8. **Health check**, within 90 s:
   - the app answers on `/run/eirl-encoder/api.sock`. If `GET /update` answers, its `app` field (the
     running binary's version) must be the new release: a VERSION file on disk under the old binary
     does not pass;
   - if it was restarted, the engine is active and has written its state again;
   - then both units must stay up, with the same InvocationID, for 10 s.
9. Healthy → marker removed and `installed`. The backup of the previous release is the only one
   kept.
10. Not healthy, or `install.sh` failed → **rollback**:
    - restore the backup, each file through a temporary name and a rename;
    - remove the absent paths;
    - `daemon-reload`;
    - restart the app. Restart the engine (never while streaming) if the install had restarted it (the
      marker says so: also when the new engine then failed, or when an earlier rollback was interrupted
      after the restore), or if the restore changed the engine's code, units or drop-in while it runs. An
      app-only bundle never restarts it;
    - `rolled_back` with the code (`unhealthy`, `install_failed`, `streaming`) and the reason. The
      restored backup is deleted (its content is installed), so the kept backup is still the previous
      release's.

Other commands:
- `eirl-update recover` runs at boot, from `eirl-update-recover.service`, which is ordered before the
  app, the engine and `eirl-hdmi-init` and only runs when the marker exists. It restores the backup
  and reloads systemd; the services then start with the old release, and the status shows
  `rolled_back: interrupted`. By hand: `sudo eirl-update -restart recover`.
- `eirl-update rollback`: the operator goes back to the kept backup. It is refused while streaming,
  and refused if that backup is not the installed release's predecessor.
- `eirl-update status` prints `status.json`.
- `eirl-update verify -manifest F -sig F -bundle F -installed V`: the board's checks, for the operator.

The helper replaces itself: the **old** helper runs the install and, if needed, the rollback. A
broken new helper therefore cannot break the normal path of the update that installs it, but it would
affect the next one. **Except after a power cut**: if `install.sh` had already replaced
`/usr/local/sbin/eirl-update`, the boot recovery runs the **new** helper's `recover`. Releases are
tested with `tools/ota-sim.sh` (which includes that recovery) before they are published.

## 8. API (`/api/v1`, web with session + CSRF, and the local socket)

| Route | Response |
|---|---|
| `GET /update` | `{"current","state","available":{"version","notes","size","released"}\|null,"progress":0-1,"lastCheck"\|null,"error"?,"message"?}`. Additive fields: `app` (running binary), `streaming`, `canInstall`, `blocked` (`streaming` \| `not_ready` \| `busy`) with `blockedMessage`, `lastResult` (`state` installed/rolled_back/failed/refused, `from`, `to`, `error`, `message`, `startedAt`, `finishedAt`), `autoCheck`, `testBuild`, `versions` |
| `POST /update/check` | `202` with the same body (the check runs in the background); `409 busy` while installing |
| `POST /update/install` | `202`; `409 streaming`, `409 not_ready` (nothing verified downloaded yet), `409 busy`; `500 helper_failed` |

- **States**: `idle`, `checking`, `available` (newer version known, not downloaded: streaming or
  waiting), `downloading`, `ready`, `installing`, `rolled_back`, `error`.
- `rolled_back` stays until a manual check (or a new install) dismisses it, also across restarts of
  the app.
- `notes` comes in the request's language (`Accept-Language`), falling back to English.
- **Error codes**: `network`, `bad_signature`, `bad_manifest`, `checksum`, `min_from`, `not_newer`,
  `unhealthy`, `install_failed`, `rollback_failed`, `interrupted`, `manual`, `helper_failed`,
  `helper_timeout`, `bad_bundle`, `backup_failed`.
- A channel with nothing published yet (the manifest URL answers 404) is not an error: the state stays
  `idle` (up to date) and the next check comes at the normal pace. A manifest without its signature is
  still `network` (a broken release).
- `message` is rendered in the request's language from the i18n catalog (`update.*`, `update.err.<code>`).
- The local socket serves the same three routes (`api/local.go` whitelist; the touch screen has the
  button). That is also how the helper's health check asks the new app for its version.

## 9. Testing

```bash
cd manager && gofmt -l . && go vet ./... && go test ./...   # manifest/signature, semver, minFrom, download,
                                                            # state machine, helper (install, rollbacks, refusals,
                                                            # power loss) on a temporary root, API handlers
```

**Local release, served over HTTP** (a throwaway key; the binaries of that build trust only it):

```bash
S=$(mktemp -d)
go -C manager run ./cmd/eirl-release keygen > "$S/key"     # seed=… and pubkey=… (THROWAWAY)
sed -n 's/^seed=//p' "$S/key" > "$S/seed"; PUB=$(sed -n 's/^pubkey=//p' "$S/key")
tools/release.sh build --local "$S/A" --skip-engine --key-stdin --pubkey "$PUB" --version 0.1.1-test.1 < "$S/seed"
tools/release.sh build --local "$S/B" --skip-engine --key-stdin --pubkey "$PUB" --version 0.1.1-test.2 < "$S/seed"
tools/release.sh build --local "$S/C" --skip-engine --key-stdin --pubkey "$PUB" --version 0.1.1-test.3 \
  --inject-failure app < "$S/seed"                          # the new app never starts → rollback
python3 -m http.server --directory "$S/B" 8000 &
go -C manager run ./cmd/eirl-release check -url http://127.0.0.1:8000/manifest.json -pubkey "$PUB" -current 0.1.1-test.1 -dir "$S/dl"
```

**On a test board** (in a slot, not streaming):
1. Install the test build A by hand: extract the bundle and run `sudo ./install.sh`. The board then
   trusts the test key and reports `testBuild: true`.
2. Point the app at the server: in `/etc/default/eirl-encoderd`, add
   `-update-url http://<machine>:8000/manifest.json`, then restart `eirl-encoderd`.
3. `POST /update/check`, wait for `ready`, `POST /update/install`, and follow
   `journalctl -u eirl-update -f`.
4. Serve C to see the rollback; `sudo eirl-update rollback` to go back one release.
5. Afterwards, reinstall a normal build (`manager/build.sh`) and remove `-update-url`.

**Simulated board (container)**: `tools/ota-sim.sh` (Go and Docker, ~3 min, never touches a board).
An Ubuntu 18.04 arm64 container runs the real `install.sh`, `eirl-encoderd` and `eirl-update`, with
fake `systemctl`/`sudo` (`tools/ota-sim/`). The engine is emulated: its restarts are counted.

It has passed these scenarios:
1. a first install by hand;
2. a healthy update driven through the web API;
3. a rollback when the new app never starts;
4. a rollback when `install.sh` fails;
5. refusals while streaming (app 409 and helper);
6. a manual rollback;
7. recovery of a killed install;
8. an install of a release that carries the engine;
9. the rollback of a release that carries the engine;
10. a manual rollback that puts back an engine deployed by hand.

Every scenario checks how many times the engine was restarted: 0 for every app-only path, 1 for an
engine install, 2 for its rollback (the new engine, then the restored one), and 1 for a manual
rollback that changes the engine's code. `--no-engine` skips 8-10 (~2 min).

**On the real board (2026-10-14)**: steps 1-8 of the board test passed with app-only test builds
(hand install, healthy update, automatic rollback in 1 min 58 s, manual rollback, recovery of an
interrupted install). The engine was never restarted. The board's systemd (237-3ubuntu10.57) re-owns a
`StateDirectory` recursively when `User=` changes.

## 10. Risks and limits

- **No A/B partitions**: the update replaces files in place. A backup, a rename for every file and
  the boot-time recovery cover a failure or a power cut during the update. They do not cover an SD
  card that dies or fills up mid-write. A failed backup refuses the update with nothing changed;
  free space is not checked beforehand (yet).
- **L4T is never updated**: a release that needs a newer L4T, driver or device tree cannot be
  delivered this way.
- **`/etc/eirl-encoder` is not backed up** (root does not touch the app's directory). A new app that
  migrates its configuration must stay readable by the old app after a rollback. The API and
  configuration contracts are additive for that reason.
- **The streaming window**: the helper checks before the backup and again right before restarting
  the engine. If the user starts streaming in between (~20 s), the whole update is undone. The
  screen could also disable "Stream" while `state = installing`.
- **The engine restart cuts the touch screen and the preview for a few seconds**, never a stream (it
  is refused then).
- **`install.sh` side effects outside `files.txt`** are not undone by a rollback. These are users,
  groups, the one-time migrations, the udev and tmpfiles reloads and `systemctl enable`. They are
  idempotent and compatible with older releases.
- **Bandwidth**: about 40 MB per update, downloaded automatically when not streaming. On a metered
  cellular uplink that may matter; a "download only on Wi-Fi/Ethernet" option is not built yet. A bundle
  that does not match its manifest (a mispublished asset) is retried after 30 min, 1 h, 2 h… up to once a
  day; automatic checks respect that wait, a manual check retries at once.
- **No revocation of old releases**: any signed release strictly newer than the installed one can be
  installed. A compromised app can request it, and whoever controls the release channel can serve it,
  so a board can be moved to an older signed release that has a known bug (never below what it runs).
  A compiled-in list of revoked versions in the helper would close this; it is not built.

## 11. Pending

- The screen's Updates page shows the `error` code; it should show `message` (already translated).
- `tools/release.sh` refuses to put the engine in a bundle unless its unit runs as `eirl-engine`. Since
  `feat/engine-system-user` (`main` 4510b85), it does.
- The first public release `v0.1.0`. The repo has to be public for
  `releases/latest` to answer without a token.
- Key rotation with a list of trusted keys (§3); a free-space check before the backup; a "Wi-Fi/Ethernet
  only" download option.
