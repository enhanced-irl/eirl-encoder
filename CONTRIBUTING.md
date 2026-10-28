# Contributing to eirl-encoder

Thanks for your interest. eirl-encoder turns a Jetson Nano into a hardware encoder: it captures HDMI
(through a TC358743 bridge on the CSI port), USB capture devices and network cameras, mixes them on a
touch screen and streams the program in H.264/AAC over SRT or SRTLA. [`README.md`](README.md) has the
map of the repository and [`DEVELOPMENT.md`](DEVELOPMENT.md) the hardware; each piece has its own document (`MIXER.md`, `MANAGER.md`,
`SOURCES.md`, `SRTLA.md`…).

Bug reports, fixes, measurements on other hardware and documentation improvements are all welcome. For
anything larger than a fix, open an issue first so we can agree on the approach before you write code.

## What you need

- **Go** at the version in [`manager/go.mod`](manager/go.mod), for the app (`eirl-encoderd`) and the
  engine (`eirl-engine-go`). The board runs **GStreamer 1.14** (NVIDIA L4T R32.7.6, Ubuntu 18.04).
- **Python 3** only for the small helper scripts (`scripts/eirl-enginectl`, `tools/`). The board runs
  **Python 3.6**: they must not use anything newer (no `dataclasses`, no walrus operator).
- **Node.js**, only for `node --check` on the web app.
- **Docker** (arm64, native on Apple Silicon) for the board builds in `build/`.
- A **Jetson Nano B01** with L4T R32.7.6, only if you want to run it for real. Everything below except
  the board builds and installs runs on a laptop.

## Test

Run all of this before you open a pull request:

```bash
# Engine: Go tests on the host and in the linux/arm64 build image (needs Docker)
build/engine.sh

# App
cd manager && gofmt -l . && go vet ./... && go test ./... && node --check web/static/app.js
```

`gofmt -l .` must print nothing. Without a board you can still see your change working:

- `manager/cmd/eirl-encoder-sim`: a simulated device (no board, no video) for working on the link with
  the EnhancedIRL cloud and on the API.
- `manager/cmd/eirl-ui-render`: draws the touch-screen interface into PNGs.

## Build and install on a board

```bash
build/engine.sh                 # Go engine for linux/arm64 → build/out/eirl-engine
JETSON=<host> manager/build.sh  # app bundle (tests + binary + install.sh) and installs it on <host>
JETSON=<host> tools/deploy-engine.sh   # installs the engine; refuses to do anything while streaming
build/build.sh                  # TC358743 driver module and device tree (downloads the L4T sources)
```

Rules for a board:

- **Never interrupt a live stream.** Check that output is off before you restart the engine;
  `tools/deploy-engine.sh` does it in the same step as the restart.
- **Never** flash the QSPI or the SD card, run `apt dist-upgrade`, or touch the bootloader from these
  tools. The `nvidia-l4t-*` packages are held on purpose (see `BASE.md`).

## How we work

- **Everything in English**: code, identifiers, comments, log messages, documentation and commit
  messages.
- **The user interface is translated** into Spanish, English and French. Every string a user can see
  (touch screen, web, API messages) needs all three.
- **Contracts are additive.** The engine's control socket (`MIXER.md` §5) and the local HTTP API
  (`MANAGER.md` §3) only gain optional fields; a new command must be answered with `unknown_command` by
  an older engine, and clients must cope with that.
- **Branches and pull requests**: branch from `main`, one topic per pull request, small focused commits.
  Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`,
  `refactor:`, `docs:`, `test:`, `chore:`). The maintainers develop in an internal repository and
  publish here; an accepted pull request may be applied there and come back with the next sync, with
  your authorship kept.
- **Documentation lives next to the code it describes**: update the document of the piece you change
  (`MIXER.md`, `MANAGER.md`…) in the same pull request.
- **No secrets** in the repository: no keys, tokens, passwords, stream keys or personal data, not even
  in test fixtures (use obviously fake values). `tools/public-check.sh` must pass.

No sign-off (DCO) and no contributor licence agreement are required. By contributing you agree that your
contribution is licensed under the licence of the files you change: Apache-2.0 for the repository,
GPL-2.0-only for `driver/` and `dt/` (see [`NOTICE`](NOTICE)).

## Security

Do not report vulnerabilities in public issues: see [`SECURITY.md`](SECURITY.md).
