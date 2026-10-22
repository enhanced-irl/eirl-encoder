> **Status:** RELEASED (the OTA since v0.1.0, Oct 14; the system layer, §12, since 0.11.0, Oct 22) · **Updated:** 2026-10-22

# Over-the-air updates

The encoder updates its own software from **GitHub Releases** of `enhanced-irl/eirl-encoder`. Releases are
signed with **ed25519**. The policy:

- **Check automatically, notify, install only on a button press**: on the touch screen, in the web, or
  from EnhancedIRL once the cloud supports it.
- **Never while streaming.**
- **Roll back automatically** if the new version does not come up healthy.

**What is updated**: our software (the app, the engine, the helpers, their units and configuration files) and, since
the system layer (§12), what is below it: our kernel modules and firmware, the Ubuntu packages added to the image's
frozen base, system files and the units' state. A board flashed with any published image reaches the same system as a
new image. **Never** the QSPI, the bootloader, `/boot` (the DTB, extlinux), NVIDIA's kernel, or a package of the base
as such (§12.11).

| Piece | Code |
|---|---|
| Versioning, manifest, signature, download, the app's state machine | `manager/internal/update` |
| Root helper: verify again, back up, install, health check, roll back | `manager/internal/update/installer`, `manager/cmd/eirl-update`, `manager/deploy/eirl-update{,-recover}.service` |
| API `/update` | `manager/internal/api/update.go`; wiring in `manager/cmd/eirl-encoderd/update.go` |
| Package and bundle | `manager/build.sh`, `manager/deploy/install.sh` (`--list-files`) |
| Release tool | `tools/release.sh`, `manager/cmd/eirl-release` |
| System layer (§12) | `manager/internal/update/syslayer`, `eirl-update system …`, the repo's `system/`, `tools/image/base.lock`, `build/system.sh`, `eirl-release layer`, `tools/system-equiv.sh` |
| Release key and notes | `release/eirl-release.pub` (and `release/eirl-release.pem`, the same key for OpenSSL), `release/notes/<version>/{en,es,fr}.md` |

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
   checks the free space, backs up every path of the old and the new files.txt, runs the bundle's
   install.sh (which applies the system layer first, §12), restarts the services,
   health check (90 s) → installed | rolled_back
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
(`internal/update.ProductionKey`, also in `release/eirl-release.pub` and, in PEM form for OpenSSL, in
`release/eirl-release.pem`; tests check the three are the same key). Nothing on the network is trusted:
not GitHub, not TLS, not the storage host GitHub redirects to. The signature covers the **exact bytes** of
`manifest.json`; the manifest pins the bundle's size and sha256. The SD card image's manifest is signed with
that same key, with its own `product` (`eirl-encoder-image`, IMAGE.md §8.1): the board's verifier refuses
it, and `eirl-release sign` refuses to sign it.

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

   The system layer's files are never in `files.txt` (an older helper would refuse them): the layer has its own
   allow-list, copies and state (§12.3, §12.8).

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
srt-live-transmit  libsrt.so.1.5    the SRT output (build/srt.sh): /usr/local/bin and /usr/lib/eirl-encoder/lib
engine/lib/*  engine/units/*        the engine as tools/deploy-engine.sh installs it (its file lists are read
                                    from that script): eirl-engine-go, eirl-engine-start, eirl-enginectl,
                                    eirl-hdmi-init.sh, the EDIDs (edid-<mode>-lpcm48k.bin, from edid/*.hex), units
engine/retired.txt                  what the engine no longer uses (deploy-engine.sh's RETIRED: the Python
                                    engine), removed by install.sh
doc/                                licences → /usr/share/doc/eirl-encoder (Go toolchain and modules, MediaMTX,
                                    srtla_send AGPL-3.0 and SRT MPL-2.0, each + where its source is), and
                                    system-SOURCES.md, the system layer's sources and written offer (§12.9)
system/                             the system layer (§12): layer.json, debs/, files/. Applied by install.sh,
                                    not in files.txt
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
#    It runs the Go tests, builds everything (the system layer too: build/build.sh, build/system.sh, eirl-release
#    layer; §12.2), signs and verifies with the production key.
#    If the system layer changed: tools/system-equiv.sh with the release's image (§12.10) before publishing.
#    EIRL_RELEASE_KEY_CMD is the command that prints the signing seed (the maintainers' secret store).
# 3. Optional: try it on a test board first (§9, with a test build)
# 4. Bring the public repository to that same commit: ONE new commit on top of its history (never a
#    force-push), with the same excluded paths and public-check. Pushing it publishes the source.
git clone https://github.com/enhanced-irl/eirl-encoder.git /tmp/eirl-public   # or pull an existing clone
tools/public-sync.sh --ref <release commit> /tmp/eirl-public && git -C /tmp/eirl-public push origin main
# 5. Publish (separate and explicit: asks you to type the version; every board is offered it within ~6 h).
#    It refuses unless the public main says "Exported from internal commit <the bundle's commit>".
tools/release.sh publish
git tag -a v<version> -m "eirl-encoder <version>" && git push origin v<version>
# 6. Check it the way a board does
go -C manager run ./cmd/eirl-release check -url https://github.com/enhanced-irl/eirl-encoder/releases/latest/download/manifest.json -current <previous>
```

When the release also gets an SD card image (IMAGE.md §8: not every release does, §8.3), after step 6:

```bash
# 7. Sign the image the recipe built with this release installed (IMAGE.md §3). The raw values are the recipe's;
#    without them it hashes `xz -dc` itself. Either way it checks them, with the release's manifest, the signature
#    (EIRL_RELEASE_KEY_CMD, as in step 2) and the .sha256 file
tools/release.sh image --image <recipe output>/eirl-encoder-<version>-jetson-nano.img.xz \
  --raw-size <bytes> --raw-sha256 <sha256> --packages <package list> --sources <sources.md> \
  --commit <recipe commit> --built <when the recipe built it, RFC3339>
#    → build/out/release/<version>/image/: eirl-encoder-<version>-jetson-nano{.img.xz, .img.json, .img.json.sig,
#      .img.xz.sha256, -packages.txt, -sources.md}. A rebuild of the image of the same release: <version>-N in its name
# 8. Add them to the release of step 5 (asks you to type the version). It never creates a release or changes which
#    one is latest, refuses a file over GitHub's 2 GiB, and never replaces a file unless --clobber
tools/release.sh publish-image <version>
# 9. Check the download as a user would (IMAGE.md §8.4)
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
4. **Free space**: twice the backup and the extracted bundle, plus 200 MB, or `refused: no_space` with nothing changed.
   Then the **backup** of every path in the old `files.txt` and the new one that exists: files and symlinks
   with owner, mode and time, into `backup-<installed>.tar` (fsynced). The paths of the new release
   that do not exist yet are recorded as *absent*, and a restore removes them. On the first OTA from
   an install that predates it, there is no old `files.txt`: the new bundle's paths are backed up.
5. **Marker** `in-progress.json`. From here on, any failure, crash or power cut brings the old
   release back.
6. Runs the bundle's **`install.sh`**:
   - non-interactive, with a fixed PATH, a minimal environment and `EIRL_OTA=1`, and a 20 min timeout (10 min in the
     helpers up to 0.9.0); it applies the system layer first (§12.3);
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
  app, the engine and `eirl-hdmi-init` (and after udev) and only runs when the helper's marker or the system layer's
  exists. It undoes an interrupted system layer apply (§12.4), restores the backup and reloads systemd, the services
  then start with the old release, and the status shows `rolled_back: interrupted`. By hand:
  `sudo eirl-update -restart recover`.
- `eirl-update rollback`: the operator goes back to the kept backup. It is refused while streaming,
  and refused if that backup is not the installed release's predecessor.
- `eirl-update status` prints `status.json`.
- `eirl-update verify -manifest F -sig F -bundle F -installed V`: the board's checks, for the operator.
- `eirl-update system apply|plan|recover|status|fingerprint|compare`: the system layer (§12).

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
| `POST /update/reboot` | Restart to finish an update (§12.5): `202 {"rebooting":true}`; `409 streaming`, `409 busy` (an update installs), `409 nothing_pending`; `503 power_unavailable`, `500 power_failed` |

`GET /update` also has, additively: `downloadWaits` (`metered`: the automatic download waits for Wi-Fi or a cable, §12.7)
with `downloadWaitsMessage`, and `system` (absent where the board has no system layer state): `release`, `appliedAt`,
`state` (`ok` \| `failed` \| `skipped`), `error` and `message` when failed or skipped, `restartRequired`, `pending` (the areas:
`hdmi`, `bluetooth`, `uplinks`, `system`) and `restartMessage`.

- **States**: `idle`, `checking`, `available` (newer version known, not downloaded: streaming or
  waiting), `downloading`, `ready`, `installing`, `rolled_back`, `error`.
- `rolled_back` stays until a manual check (or a new install) dismisses it, also across restarts of
  the app.
- `notes` comes in the request's language (`Accept-Language`), falling back to English.
- **Error codes**: `network`, `bad_signature`, `bad_manifest`, `checksum`, `min_from`, `not_newer`,
  `unhealthy`, `install_failed`, `rollback_failed`, `interrupted`, `manual`, `helper_failed`,
  `helper_timeout`, `bad_bundle`, `backup_failed`, `no_space`.
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

**The system layer** (§12.13): `go test ./internal/update/syslayer`, and `tools/system-equiv.sh` against every published
image (§12.10).

**The SD card image's signature**: `tools/image-sign-test.sh` (Go and xz, about a minute, no board, no GitHub). A
throwaway key, a fake 64 MiB image through `tools/release.sh image`, `verify-image`, one byte changed in the image,
the manifest or the raw stream, OpenSSL 3 with the PEM key, and every refusal of `publish-image` against a stubbed
`gh` (IMAGE.md §8.1).

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

**Image mode (SD card image)**: the image build runs a bundle's `EIRL_IMAGE=1 ./install.sh` (or `--image`) in an
arm64 chroot with /proc, /sys and /dev mounted, no systemd running in it and no network (no `--root`: the chroot
gives the paths). It installs the files, users and groups and runs `systemctl enable`
(offline), and never `daemon-reload`, start/restart/stop, `udevadm`, `systemd-tmpfiles`, the NetworkManager
dispatcher or anything under `/sys` and `/dev`; all of that happens at the first boot. It refuses to run where
systemd runs that root. `tools/image-install-test.sh [BUNDLE]` (Docker, ~1 min; without a bundle it first builds
a test release with the engine) runs the real `install.sh` in image mode twice in that same setting (a privileged,
offline Ubuntu 18.04 arm64 container is the build machine, with /run/systemd/system; a copy of its root is the
chroot) with guards on every forbidden command, and checks files.txt, the SRT output (it runs with the package's
libsrt), the EDIDs, the licences, a new device's settings (output off), the users, the units enabled for the
first boot, `eirl-encoderd -print-hardware-id`, that the second run changes nothing and the refusal.

**On the real board (2026-10-14)**: steps 1-8 of the board test passed with app-only test builds
(hand install, healthy update, automatic rollback in 1 min 58 s, manual rollback, recovery of an
interrupted install). The engine was never restarted. The board's systemd (237-3ubuntu10.57) re-owns a
`StateDirectory` recursively when `User=` changes.

**First public release (Oct 14).** `v0.1.0` is published at
<https://github.com/enhanced-irl/eirl-encoder/releases/tag/v0.1.0>, signed with the production key.
The test board, on a development build of the same day, then went through the real path on its own:
check → download (44 MB, ~30 s) → verify → install of the app and the engine → healthy, in 31 s, with
nothing streaming.

## 10. Risks and limits

- **No A/B partitions**: the update replaces files in place. A backup, a rename for every file and
  the boot-time recovery cover a failure or a power cut during the update. They do not cover an SD
  card that dies or fills up mid-write. A failed backup refuses the update with nothing changed;
  free space is not checked beforehand (yet).
- **A system layer that cannot apply on one board stops that board's update** (§12.4, as decided): a check that fails
  before anything changes (a dependency left unmet by something installed by hand, dpkg broken) or an apply that is
  undone rolls the whole release back. That board stays on its release, the screen and the web say why, and only a
  release whose layer fixes it moves it on. The other way, installing the app over the previous layer, was not chosen:
  a new app may need its layer.
- **The bundles of 0.7.0 to 0.10.0 were refused by every board** (found on Oct 21 by `tools/system-sim.sh`): their
  `files.txt` named four files of Bluetooth audio outside the helper's allow-list (§3): BlueALSA's ALSA plugins, their
  `alsa.conf.d` file and the D-Bus policy. Every helper since 0.1.0 refuses such a bundle whole (`bad_bundle`, "the update
  package is not valid"), before changing anything, so no board updated over the air after 0.6.1 (the test board got
  0.7.0 by a new card). Since the system layer those four files are the layer's (`system/built.txt`,
  `system/overlay.txt`) and `files.txt` is again within the allow-list, so the first release with the layer installs on
  every board, whatever image and release it runs. `tools/release.sh build` now refuses a `files.txt` with a path the
  helper would refuse (`eirl-release check-files`).
- **Below the system layer nothing moves** (§12.11): a release that needs another L4T, kernel Image, bootloader or
  device tree cannot be delivered this way. Kernel modules, firmware and Ubuntu packages can (§12).
- **`/etc/eirl-encoder` is not backed up** (root does not touch the app's directory). A new app that
  migrates its configuration must stay readable by the old app after a rollback. The API and
  configuration contracts are additive for that reason.
- **The streaming window**: the helper checks before the backup and again right before restarting
  the engine. If the user starts streaming in between (~20 s), the whole update is undone. The
  screen could also disable "Stream" while `state = installing`.
- **The engine restart cuts the touch screen and the preview for a few seconds**, never a stream (it
  is refused then).
- **`install.sh` side effects outside `files.txt`** are not undone by a rollback. These are users,
  groups, the one-time migrations, the udev and tmpfiles reloads and `systemctl enable`, and the system layer once
  committed (§12.4). They are idempotent and compatible with older releases.
- **Bandwidth**: about 52 MB per update (the system layer is ~1.5 MB of it), downloaded automatically when not
  streaming and not on a metered link (a modem or a phone: then it waits for Wi-Fi or a cable, §12.7). A bundle
  that does not match its manifest (a mispublished asset) is retried after 30 min, 1 h, 2 h… up to once a
  day; automatic checks respect that wait, a manual check retries at once.
- **No revocation of old releases**: any signed release strictly newer than the installed one can be
  installed. A compromised app can request it, and whoever controls the release channel can serve it,
  so a board can be moved to an older signed release that has a known bug (never below what it runs).
  A compiled-in list of revoked versions in the helper would close this; it is not built.

## 11. Pending

- Key rotation with a list of trusted keys (§3). (The free-space check and the download that waits for Wi-Fi or a
  cable came with the system layer, §12.7.)

## 12. The system layer

The SD card image is only an installation medium: a board flashed with **any** published image (0.7.0, 0.8.0) reaches,
over the air, the same system as a board flashed with the image of the latest release. Until 0.8.0 a release that needed
something below our software (a kernel module, firmware, an Ubuntu package, a drop-in) needed a new image too: Bluetooth
audio in 0.7.0 (`bluez`, the patched `btusb`/`btrtl`, the RTL8761B firmware), an iPhone over USB in 0.8.0 (`ipheth`,
`usbmuxd`, its drop-in). Since this layer, every bundle carries that part of the system as well.

```
QSPI, bootloader, the card's boot partitions          never (§12.11)
/boot (DTB, extlinux), NVIDIA's Image and initrd      frozen with the base (§12.11)
the base: Ubuntu 18.04 + L4T R32.7.6 (tools/image/base.lock = the image of 0.7.0)   frozen
the system layer: our modules + depmod, firmware, the packages added since, system files, units   OTA (this section)
our software: app, engine, helpers (files.txt)        OTA (§1-§11)
```

### 12.1 Decisions (Oct 19)

| # | Question | Decided |
|---|---|---|
| D-1 | Format | Everything inside the signed bundle (`system/`), only what the frozen base lacks: ~1.4 MB compressed today (the 8 modules ~1.3 MB, firmware 46 KB, the 4 packages of the iPhone 123 KB), +3 % on a bundle of ~50 MB. Not a signed list of Ubuntu downloads: bionic still publishes now and then and drops what it supersedes, and a board that comes online a year later must still install |
| D-2 | One source of truth | The image is the frozen base plus the same layer, applied by the same code: the image's `install.sh --image` runs `eirl-update system apply -image`. Any change to Ubuntu (a base package's update too) goes through the layer. Checked at every release by `tools/system-equiv.sh` (§12.10) |
| D-3 | Installing `.deb` files offline | `apt-get` with local files only (no source at all, its own empty lists and cache), a simulation that must pass first, `dpkg` underneath (§12.3) |
| D-4 | Rollback | A transaction while it applies (a failure or a power cut undoes it); forward only once committed: a rollback of the app does not undo it, so a change of the layer keeps the previous release working (§12.4) |
| D-5 | Reload and restart | Nothing in use is reloaded or restarted. What waits for a restart is shown on the screen and the web ("Restart to finish the update"), with a button that is refused while streaming. Never automatic (§12.5) |
| D-6 | Migration and drift | Decided by the board's state, never by its version; no `minFrom`. What the board has and the layer does not list is reported (drift), never removed (§12.6) |
| D-7 | Space and metered links | The space is checked before anything changes; the automatic download waits for Wi-Fi or a cable (§12.7) |
| D-8 | Security | The same trust model (§3): the layer is data under the release's signature; its own allow-lists, the packages checked against their sha256 and control, the modules against the kernel (§12.8) |
| D-9 | Licences | The layer's `SOURCES.md` in the bundle's `doc/`, and copies of every new source package next to the image's (§12.9) |
| D-10 | DTB and extlinux | Frozen with the base: L4T's U-Boot cannot go back to an earlier entry by itself (§12.11) |
| D-11 | The kernel's Image and initrd | Frozen: NVIDIA's, on hold, and R32.7.6 is the last release (§12.11) |
| D-12 | The version the system says | The layer writes `os-release`, `lsb-release` and `issue` with the installed release (§12.12) |

### 12.2 Where it comes from

| File | What |
|---|---|
| `tools/image/base.lock` | The frozen base: the packages of the image of 0.7.0 (recipe `0f07c8317eb5`), exactly. It never changes. Its `.deb` files are kept by the maintainers (`tools/image/base-debs.sh`, 474 packages, Oct 21), so the image can be rebuilt even when Ubuntu stops serving a version (`tools/image/build.sh --base-debs`) |
| `system/packages.txt` | The Ubuntu packages added on top of the base, with why (`usbmuxd` today) |
| `system/packages.lock` | Their closure over the base, exactly: name, version, arch, sha256, size, installed size, source package and version, path in Ubuntu's archive. `build/system.sh --update-lock` resolves it in an Ubuntu 18.04 arm64 container whose dpkg database is the base (each package's record from Ubuntu's signed archive) |
| `system/built.txt` | What comes from `build/out`: our modules and firmware (`build/build.sh`), BlueALSA's ALSA plugins and their configuration (`build/bluealsa.sh`). Where each goes, which file it is, its area and its flags (`reboot`, `usedBy=`) |
| `system/modules.lock` | The sha256 of every module `build/build.sh` builds. Its builds are reproducible (the 8 modules of the test board, built on Oct 16, and those built on Oct 21 are the same bytes), so a changed module is a reviewed change, never an accident |
| `system/overlay.txt` + `system/files/` | System files: the first-boot units and scripts of the image, the journal's and NetworkManager's drop-ins, usbmuxd's (MODEMS.md §12.2), and the system's name (`@RELEASE@` becomes the release) |
| `system/units.txt` | The units' state: what is masked, disabled or enabled (never `ssh`, nor the app's units) |
| `system/groups.txt` | System groups (`debug`) |

`build/system.sh` fetches the `.deb` files of the lock from Ubuntu's archive and checks their sha256. `eirl-release layer`
(from `tools/release.sh build`, unless `--skip-system`) writes `system/` of the bundle: `layer.json` (everything above,
plus the base's package list), `debs/` and `files/`, and `doc/system-SOURCES.md`. A file of `system/files/` that
`overlay.txt` does not list, a module that differs from `modules.lock`, or a package that differs from its lock stops it.

**Changing the layer**: a package → `system/packages.txt`, then `build/system.sh --update-lock` and review the lock's diff;
a module → `driver/` and `build/build.sh`, then `eirl-release layer -update-modules-lock` and review; a file →
`system/files/` and a line in `overlay.txt`; a unit → `units.txt`. Then `tools/system-equiv.sh` (§12.10).

### 12.3 How it is applied

`install.sh` does it first, when the bundle has `system/`: it installs the update helper and its units, then runs
`/usr/local/sbin/eirl-update system apply -layer system/` (`-image` in an image's chroot). The helper goes first so that a
power cut from then on is undone at boot by a helper that knows the layer. The layer is **not** in `files.txt`: an older
helper (0.7.0, 0.8.0) refuses a `files.txt` with a path outside its allow-list (`/lib/modules`, `/lib/firmware` are), and
it is the older helper that runs the first release with a layer (§7). The layer keeps its own state in
`/var/lib/eirl-system/`: `state.json` (0644: what it owns, what waits for a restart, drift, the last error), the marker of
an apply in progress and its copies (`txn/`, 0700).

1. **Plan**: the layer against the board. Files by sha256, packages by dpkg's version (an installed version newer than the
   layer's is left alone: drift), units by their links, groups. Nothing to do → it records the state and stops.
2. **Checks, nothing changed yet**: the base is the encoder's (§12.6); dpkg's lock free (waits up to 60 s) and nothing
   left half done (`dpkg --audit`, else `dpkg --configure -a` first, before the plan: a package an earlier cut left
   unpacked is the board's, not something this apply adds); each file of the layer has its sha256 and each module the
   kernel's vermagic; each `.deb` its size, sha256 and control (`dpkg-deb -f`: Package, Version, Architecture); the
   space (§12.7); `apt-get -s` must resolve the packages against what the board really has.
3. **Copies and marker**: every file it replaces or removes, the units' previous state, the packages it adds (new) or
   upgrades (their `.deb` kept, to complete them if the apply is undone). The marker is written last.
4. **Packages**: `apt-get install` of the local `.deb` files with no source, its own lists and cache, `--no-remove`, keeping
   changed configuration files (`--force-confold`), under a temporary `policy-rc.d` that keeps every maintainer script
   from starting a service (no `policy-rc.d`, no packages). Users that a package creates stay (by name: uids may
   differ from an image's). Packages go before files, so a package that ships one of the layer's paths never
   overwrites the layer's file.
5. **Files** (a temporary name and a rename each), `depmod -a 4.9.337-tegra` if a module changed.
6. **Units** (`systemctl enable|disable|mask|unmask`), **groups**, and on a running board `daemon-reload` and
   `udevadm control --reload` (never `trigger`).
7. **Verify**: the plan again, which must now be empty. **Commit**: `state.json`, marker and copies gone.
8. **Retire** what an earlier layer added and this one no longer lists (purged after the commit; if that fails it stays
   listed and the next apply tries again). A base package is only removed when the layer names it in `remove`.

The old helper gives `install.sh` 10 minutes; the layer of the first release takes seconds (4 small packages). An apply has
its own deadline of 7 minutes, after which it undoes itself, so a slow apply never outlives the `install.sh` that started
it. Once it starts changing the board it ignores SIGTERM and SIGINT (it goes to the end or undoes itself); undoing never
runs on a cancelled context. The new helper gives `install.sh` 20 minutes (its unit 30) and runs it in its own process
group, killed whole at the time limit. `eirl-update system plan -layer DIR` shows what an apply would change.

### 12.4 Rollback and recovery

| | A failure or a power cut while it applies | After the commit: the app does not come up, or a manual rollback |
|---|---|---|
| Files (`.ko`, firmware, configuration) | Restored from the copies, `depmod` | Stay |
| Packages the apply added | Purged | Stay |
| Packages the apply upgraded | Stay at the new version, configured (no older `.deb` to go back to) | Stay |
| Packages it retires | Only after the commit | — |
| Units' state | Restored | Stays |
| Users and groups | Stay | Stay |
| A module loaded in memory | Not touched | Not touched |

A failure in steps 4-7 undoes the apply and `eirl-update system apply` exits 2: `install.sh` stops, and the helper puts the
previous release back (§7). The copies include `state.json`, so a cut right after the commit wrote it still leaves the
previous state. Undoing never gets stuck: a package it cannot purge (one that an upgraded package now depends on) or a
maintainer script that fails leaves those packages in place, says so in `state.json`, and the marker goes anyway (doing
it again would fail the same way at every boot and block every later apply); only a file or a unit it cannot put back
keeps the marker for the next recovery. A file the base's packages also ship (the system's name, `base-files`) is never
removed when a layer stops listing it. A power cut leaves the marker: at boot, `eirl-update-recover.service` (now also when only the
layer's marker is there, and after `systemd-udevd`, which the packages' scripts need) undoes the layer first and then the
release's files. `eirl-update install` does the same before an install. Undoing is idempotent: cut again, it runs again.
The last error stays in `state.json` (`error`, `failed`, `failedAt`) and the web and the screen say it (§8).

Once committed the layer only moves forward, so **a change of the layer must keep the previous release working**: it adds,
it does not take away something the previous app needs. The same rule already held for users and `systemctl enable` (§10).

### 12.5 What waits for a restart

Nothing that is in use is reloaded or restarted, streaming or not. A change acts at once when nothing holds it: a module that
is not loaded (`ipheth` without an iPhone, the modems' without a modem: the next time the device is plugged in, udev loads
the new one), a drop-in of a unit that is not running (`daemon-reload`). Otherwise it waits for a restart:

- `tc358743` always (the camera input keeps it: `reboot` in `built.txt`);
- a module that is loaded now (`/sys/module/<name>`), and firmware whose module is loaded;
- a configuration file whose unit runs now (NetworkManager, journald, usbmuxd with an iPhone), unless only its comments
  changed;
- an upgraded package.

They are recorded in `state.json` with this boot's id, so they disappear by themselves after the next boot. `GET /update`
says `system.restartRequired` and the areas (`hdmi`, `bluetooth`, `uplinks`, `system`) in a sentence of the request's
language; the screen's Updates page and the web's update card show it with a Restart button: `POST /update/reboot`, refused
while streaming (409 `streaming`), while an update installs (409 `busy`) and when nothing waits (409 `nothing_pending`).
Never automatic.

### 12.6 Migration and drift

Everything is decided from the board's state, never from the version it runs: the first release with a layer leaves a board
flashed with the image of 0.7.0 (whatever release it runs), one flashed with 0.8.0 and the test board equal to a new image.
Measured in a container on the image of 0.7.0 (Oct 21): it installs `usbmuxd`, `libimobiledevice6`, `libplist3` and
`libusbmuxd4`, `ipheth.ko` and usbmuxd's drop-in, runs `depmod`, writes the system's name; a second run changes nothing.

- **No `minFrom`**: a board below it can only be reflashed (§3), which is what this layer exists to avoid.
- **Drift**: what the board has that neither the base nor the layer lists (the test board's `libimobiledevice-utils`,
  installed by hand), base packages at another version, a layer package installed at a newer version. Reported in
  `state.json`, never touched.
- **Not the encoder's base** (another L4T than 32.7.6, no `/lib/modules/4.9.337-tegra`, another architecture): the layer is
  skipped (`skipped` in `state.json`, said on the screen and the web) and the release's software installs as before.
- `ssh` is never in the layer: the debug image runs it, a product image masks it.

### 12.7 Space and metered links

The layer checks, before changing anything, that the card has twice what it needs (the packages' installed size, its files,
the copies) plus 300 MB. The helper checks the same for the release (the extracted bundle and the backup, twice, plus 200 MB)
before its backup: `refused: no_space`, nothing changed (§11 had this pending).

The app's automatic download waits while the way to the internet is metered: the default route (lowest metric) goes
through a modem (`wwan*`) or a USB network device that is not an Ethernet adapter (a HiLink modem, a phone or an iPhone
sharing its data). `GET /update` then says `downloadWaits: "metered"` with its sentence; a check from the screen or the web
downloads it at once, by any link. A release costs ~52 MB; the layer adds ~1.5 MB to it.

### 12.8 What it may touch

The trust model of §3 does not change: the layer is data inside the bundle whose sha256 the signed manifest pins, and the
board accepts no other source (no apt source, nothing from the network). The release key could already do anything on a
board (`install.sh` runs as root): the layer gives it no new power, only more surface, the maintainer scripts of Ubuntu's
packages (the same ones that run when the image is built, exercised by §12.10 at every release). On top of the signature:

- **Files**: only `/lib/modules/4.9.337-tegra/extra/tc358743.ko`, `/lib/modules/4.9.337-tegra/updates/eirl-*/`,
  `/lib/firmware/`, `/etc/systemd/system/<unit>.d/90-eirl*.conf`, `/etc/systemd/journald.conf.d/90-eirl-*`,
  `/etc/NetworkManager/conf.d/`, `/usr/lib/eirl-image/`, the image's two first-boot units and the system's name files.
  Never `/boot`, `/etc/eirl-encoder`, sudoers, `/home`, dpkg's database or the app's files.
- **Packages**: never `nvidia-l4t-*`, `jetson-*`, a kernel or a bootloader; `apt` refuses to change one on hold.
- **Units**: never `ssh` or the app's own (`eirl-*` but the image's first-boot units).
- **Modules**: built for 4.9.337-tegra (vermagic), checked before anything changes.

### 12.9 Licences

Distributing `.deb` files and firmware in the bundle carries the image's obligations (IMAGE.md §9.3). Every bundle with a
layer has `doc/system-SOURCES.md` (installed in `/usr/share/doc/eirl-encoder/`): the written offer (three years after the
last distribution, GPL, LGPL and AGPL, support@enhancedirl.com), and where the source of each thing is: each package's
source package and version, NVIDIA's `public_sources.tbz2` and our patches for the modules, linux-firmware for the firmware.
Each package's licence comes with it (`/usr/share/doc/<package>/copyright`). In each release that changes the layer's
packages, their source packages go into the maintainers' copies (`backups/eirl-encoder-image-sources/ubuntu/`), checked
against their `.dsc`, as for the image of 0.8.0.

### 12.10 Equivalence: the fingerprint

`eirl-update system fingerprint` describes a root filesystem as the encoder sees it: packages and versions (and those on
hold), our modules (sha256) and `modules.dep`, firmware, enabled and masked units, the layer's files, groups and users by
name, and `/boot` (extlinux, the DTB without its build time, §12.11). `eirl-update system compare -a A -b B -expected F`
lists the differences and fails on any that `F` does not allow.

`tools/system-equiv.sh --release DIR --image NEW.img.xz` (or `--report` of the image's build, whose `check.py` writes
`fingerprint.json`) takes every published image (verified with the release key), gives its root filesystem the new
release in a container the way the helper does (`install.sh`, `EIRL_OTA=1`, twice: the second must change nothing), and
compares its fingerprint with the new image's. `tools/system-equiv.expected` lists what may differ, each with its reason.
Run it for every release that changes the layer, before publishing it.

Oct 21, a test release of the first layer (0.11.0-test.1) and the test image the new recipe built from it (0 of its checks
failed): the published images of 0.7.0 and 0.8.0, given that release over the air, have **the new image's fingerprint
exactly** (0 differences, nothing in `system-equiv.expected`). On the way it caught two NVIDIA units the old recipe
disabled by hand (`nvweston`, `nvmemwarning`: now in `units.txt`) and the DTB's build time (§12.11).

### 12.11 What stays frozen, and why

- **The QSPI, the bootloader and the card's boot partitions**: never (the board's rules).
- **The DTB and extlinux** (`/boot`, in the root filesystem): frozen with the base, the layer never writes `/boot`. L4T
  R32.7.6's U-Boot (2020.04, read in NVIDIA's sources) has no boot counter (`CONFIG_BOOTCOUNT_LIMIT`), no "boot this once",
  no `fdtoverlays` in extlinux and no USB keyboard, and keeps its environment in the QSPI. A new entry made the default with
  a DTB that does not reach Linux cannot be undone by any software: choosing the earlier entry takes a serial cable or the
  card in another computer. That breaks "never leave the board without a boot". The DTB has not changed since Oct 9; a change
  of the `tc358743` driver must work with the frozen DT. `build/build.sh` now builds the DTB with a fixed
  `SOURCE_DATE_EPOCH` (NVIDIA's tree records its build time in `nvidia,dtbbuildtime`), so it is byte for byte the image of
  0.7.0's, and `tools/image/pins.sh` pins its sha256: the image's checks refuse another one. The splash's extlinux entry
  (a copy of the default one with other kernel arguments) stays as it is (§4).
- **NVIDIA's Image and initrd** (`nvidia-l4t-kernel`, on hold): R32.7.6 is the last release, there will be no other Image;
  changing it would invalidate every module (vermagic) and has the same problem as the DTB. Everything of ours is an
  out-of-tree module.
- **The base's packages**: never upgraded as such; a newer version of one goes through the layer (§12.2).

### 12.12 The version the system says

`/usr/lib/os-release` (`/etc/os-release` links to it), `/etc/lsb-release`, `/etc/issue` and `/etc/issue.net` are the layer's:
they say the installed release ("EnhancedIRL Encoder 0.10.0"). Before, a board flashed with 0.7.0 said 0.7.0 forever. What a
card was flashed with is in `/usr/lib/eirl-image/flashed` (images from the new recipe on).

### 12.13 Tests

- `go test ./internal/update/syslayer`: format and allow-lists, plan, a whole apply on a temporary root with a fake dpkg,
  apt and systemctl, a failure in the middle and its undo, a power cut and the recovery, retiring, drift, the base check, the
  pending restarts (also comment-only changes), the fingerprint and its comparison, the DTB without its build time, and
  `eirl-release layer`.
- `go test ./internal/update ./internal/api`: the metered link and the download that waits; `GET /update`'s `system` and
  `downloadWaits`, `POST /update/reboot`.
- In a container, the root filesystem of the published image of 0.7.0 gets a test release: the layer applies for real
  (`apt`, `dpkg`, `systemctl`, `depmod`), a second run changes nothing (Oct 21).
- `tools/system-equiv.sh` (§12.10).
- `tools/system-sim.sh`: the root filesystem of the published image of 0.7.0 in a container runs the real helpers, the
  older one built from v0.8.0 first. A power cut with the layer's packages unpacked and not configured (dpkg holds after
  unpacking; every process killed) is undone at boot by the new helper: the packages purged, the files, `os-release` and
  the release back, nothing left half done in dpkg. Then the older helper installs the first release with a layer; an
  app that never starts is rolled back while the committed layer stays; a layer with a file that does not match is
  refused before changing anything; streaming is refused with the layer untouched.
- `tools/ota-sim.sh` passes again (it could not since 0.7.0: §10).
- On the test board (Oct 22, test releases with a throwaway key): the iPhone packages installed there by hand removed
  with dpkg, as on a board flashed with the image of 0.7.0, and the code of 0.7.0 installed by hand. Its helper installed
  the first release with a layer in 74 s: the 4 packages through apt inside the real `eirl-update.service` (its sandbox
  included), 4 files, nothing waiting for a restart, healthy. A release whose layer has a file that does not match:
  refused before changing anything, rolled back. A healthy one: installed. The board's fingerprint is then the new
  image's, except what its debug image adds (its SSH user, sshd) and the release's name.
