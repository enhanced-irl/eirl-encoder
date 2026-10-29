> **Status:** RELEASED in 0.12.0 (Oct 28): R1-R4 and R6's release; the cloud's view (D8) and a power cut by the cable still to do
> **Updated:** 2026-10-28

# Local recording to a USB disk

The encoder records the **program** (what goes on air, with its audio) to a USB disk plugged into the board, with
its own high-bitrate encode, independent of the stream. If the link goes down, the stream stops but the recording
goes on: a local backup. It can also record without streaming. This document is the contract between the pieces
(the storage helper, the engine, the app) and the record of what was measured.

## 1. Decisions (Oct 24)

| # | Question | Decided |
|---|---|---|
| D1 | The setting | **Local recording**: off · independent (a REC button, with or without the stream) · with the stream (Go live starts both, Stop stops both). REC is there in both modes |
| D2 | Default quality | H.264 High 25 Mbps VBR, AAC 256 kbps, keyframe every 1 s, the program's size and rate. Also standard (15 Mbps), max (40 Mbps) and efficient (H.265 20 Mbps) |
| D3 | Container | **Fragmented MP4** (1 s fragments): opens in Premiere, DaVinci Resolve, Final Cut and QuickTime |
| D4 | Files | **One file per recording**; on FAT32 a new file at 3.9 GB |
| D5 | Filesystems | exFAT, FAT32 and ext4; formatting to exFAT from the device, with confirmation. NTFS is not supported |
| D6 | After a power cut | The recording **stays stopped** (the stream resumes with `OUTPUT=on`); at start the engine says why (`power_lost`) |
| D7 | Disk full | Stop and say so (a warning 10 minutes before) |
| D8 | Cloud | The state in the report (R3); remote control later (R6) |

## 2. How it fits

```
program ─ tee vt ─▶ appsink outv ┐ bridges ─▶ [output session]  NVENC 4-8 Mbps + ABR ─▶ TS ─▶ sender ─▶ link
audio   ─ tee at ─▶ appsink outa ┘
        ─ tee vt ─▶ appsink recv ┐ bridges ─▶ [record session]  NVENC 25 Mbps VBR + AAC ─▶ mp4mux (fragmented)
        ─ tee at ─▶ appsink reca ┘                                  ─▶ appsink rout ─▶ writer (Go) ─▶ disk
```

The recording is a second hot output, the output's sibling (MIXER.md §9): its own pipeline per session on the
program's clock and `base_time`, fed by pass bridges, created when recording starts and destroyed when it stops.
A failure of one never touches the other. The muxed MP4 (~3 MB/s) enters Go through an appsink: frames never do.

## 3. Measured on the board (R0, Oct 24)

| What | Result |
|---|---|
| NVENC alone (H.264 1080p, 25 Mbps) | 160 fps |
| NVENC with the stream on air (1080p60, H.265 6 Mbps) | 135 fps left; the stream stays at 59.9 fps |
| Stream + a test recording on exFAT, 2 min | Stream at 60 fps; 363 MB (24 Mbps), every fragment complete |
| CPU | Engine 53 % of a core idle, 72 % streaming; the test recording in another process +11 points of the system (4 cores) |
| exFAT through FUSE (`exfat-fuse` 1.2.8) | 1.5-1.9 % of a core |
| A USB 3 pendrive (Kingston DataTraveler 3.0, 64 GB) | 25 and 40 Mbps sustained on ext4, FAT32 and exFAT, but `fsync` stalls up to 6.5 s |
| `mp4mux` 1.14.5, `fragment-duration=1000 streamable=true` | Valid; nearly every video fragment starts with a keyframe (not all: §8.2); QuickTime opens it |
| A real power cut, without `fsync` | Of 60 s written, 31 s were on the disk. QuickTime does not open the file until it is cut at its last complete fragment (1.8 KB lost); ffmpeg does |
| Power | Two `TEGRA_POWER_ON_RESET`: streaming + a flat-out encode + one more capture card, shortly after plugging the pendrive; and plugging a 2.5" hard disk without its own power, at rest |

What follows from it: a 64 MB write buffer, an `fsync` after every fragment, cutting an unfinished file at its last
complete fragment, `exfat-fuse` instead of a kernel module, no lowering of the stream while recording, and "plug the
disk in before going live; a hard disk only through a powered hub".

## 4. The storage helper (`eirl-storage`, root)

The image has no automounter (udisks2 is removed). A small root helper mounts one USB disk at a time and says what
it found in a state file; the engine and the app only read that file. The app reaches the two actions it needs
(eject, format) through `sudo` with exact commands.

### 4.1 Pieces

| Piece | Where |
|---|---|
| The helper | `manager/cmd/eirl-storage` → `/usr/local/sbin/eirl-storage` (root, Go) |
| udev rule | `manager/deploy/71-eirl-storage.rules` → `/etc/udev/rules.d/`: a USB **disk** (not each partition) starts `eirl-storage@<disk>.service` |
| Unit | `manager/deploy/eirl-storage@.service`: `Type=oneshot`, `RemainAfterExit=yes`, `BindsTo=dev-%i.device`, `ExecStart=… attach %I`, `ExecStop=… detach %I` and `ExecStopPost=… detach %I` (a disk pulled out while it is checked gets no `ExecStop`); no sandboxing (a mount namespace of its own would hide the disk from the engine) |
| Eject and format | `manager/deploy/eirl-storage-{eject,format}.service`: `sudo eirl-storage eject|format` checks for a recording and starts the unit, which does the work in the system's mount namespace (from the app's sandbox the unmount would stay in the app's namespace) |
| Packages | `system/packages.txt`: `exfat-fuse`, `exfat-utils`, `dosfstools` (the layer, UPDATES.md §12.2). The lock adds `libfuse2` and `fuse`, which `exfat-fuse` needs: `fuse` brings a setuid `/bin/fusermount` (MANAGER.md §5) |
| sudoers | the app (`eirl-encoder`) may run `eirl-storage eject` and `eirl-storage format`, nothing else |

### 4.2 Rules

- Only a disk on the USB bus (checked in sysfs by the helper, not only by udev), named `sd[a-z]`; never the boot device.
- Partitions and whole-disk filesystems smaller than 1 GB are ignored (a Windows installer's 1 MB `UEFI_NTFS`).
- The largest partition with a supported filesystem is the one mounted. One disk at a time: a second one leaves the
  first's state as it is and only adds `anotherDisk` (its name); when the first goes, the second takes over.
- Mounted at `/media/eirl-rec` with `nosuid,nodev,noexec,noatime`. exFAT and FAT32 with `uid=eirl-engine`,
  `gid=eirl-encoder`, `fmask=0137`, `dmask=0027`; ext4 as it is, with the recordings' folder owned by the engine and
  setgid (2750, group `eirl-encoder`): the engine's files take the app's group, as the mount gives them elsewhere.
- The recordings' folder is `EnhancedIRL/` at the root of the disk, created by the helper.
- A read-only check of the filesystem when mounting (`fsck.exfat`, `fsck.vfat -n`, `e2fsck -n`): `dirty` is reported,
  never repaired.
- A quick write test when mounting: 32 MB with an `fsync` every 8 MB, in `EnhancedIRL/`, removed afterwards → `writeMBps`.
  The engine watches the real stalls while it records (§5.4).
- `eject` refuses while the engine records (`/run/eirl-engine/mixer.json`, `record.mode` = `on`, or `record.state` =
  `starting` or `on`): exit 3. Otherwise `sync`, unmount, state `ejected`.
- `format` refuses while recording (exit 3): one MBR partition of type 7 (GPT above 2 TiB), exFAT labelled `EIRL-REC`,
  then the disk's unit restarts and mounts it. It takes the `seq` of the disk the user confirmed (on stdin from the
  app; `eirl-storage-format@<seq>.service` gets it as its argument) and formats only if the state still has that `seq`,
  checked under the lock: a disk pulled out after the confirmation, or another one that took its place, is never the
  one formatted (exit 5). The sudoers rule stays one exact command.
- Exit codes: 0 done, 1 failed, 2 no disk, 3 recording, 4 busy (a disk is being checked), 5 the disk changed (format),
  64 usage.
- `detach` with the disk still there (a shutdown, a restart of its unit) waits up to 20 s while the engine runs and
  records, so that the file's last fragments reach the disk; and only a disk that is gone hands over to a second one.
- ext4 is mounted with `errors=remount-ro` as well, over the superblock's own setting (a disk set to panic on an
  error could reboot the board).
- A disk already plugged in while the software is installed is mounted after replugging it or a reboot (the install
  reloads udev's rules but never triggers them).
- A disk pulled out without ejecting: `detach` unmounts lazily (`umount -l`) and the state goes back to `none`.

### 4.4 Limits (a review of the helper, Oct 28)

What a review of the helper and its boundary found and left as it is, for now:

- Root reads a disk that comes from outside: `exfat-fuse` 1.2.8 (from 2017, with no security updates) runs as root for
  as long as the disk is mounted, and so do the checks (`fsck.exfat`, `fsck.vfat -n`, `e2fsck -n`, `blkid -p`) and the
  kernel's drivers. A memory bug in any of them is code run as root from a crafted disk. It needs physical access to
  the board, which already allows worse (taking the microSD card). Running the checks in a sandbox can come later.
- `fuse` installs a setuid `/bin/fusermount` that nothing here uses (the helper mounts as root and unmounts with
  `umount`). No way to escalate with it was found; the system layer could drop the bit (`dpkg-statoverride`).
- `detach` goes on without the lock after 60 s (a disk pulled out while another disk's check runs), and in the
  second disk's branch it may write a state over a newer one: `checking` would stay until the disk is plugged in again.
- An ext4 disk made by a recent Linux may not mount on the board's kernel 4.9 (`orphan_file`, `metadata_csum_seed`):
  `error`/`mount_failed`. Formatting it on the encoder (exFAT) solves it. Not tried on the board.
- The app waits 120 s for a format; on a slow disk it can answer `format_failed` for a format that then succeeds (the
  state file says how it ended).
- A world-writable root folder on a crafted ext4 disk would let a local user replace `EnhancedIRL` by a link after the
  mount; the engine would then write its recordings there (as the engine's user, never root).

### 4.3 State file

`/run/eirl-storage/state.json`, 0644 (folder 0755), written whole (temporary name and rename) on every change.

```json
{"state": "ready", "reason": "", "seq": 7, "device": "sda", "partition": "sda1", "model": "DataTraveler 3.0",
 "fs": "exfat", "label": "EIRL-REC", "uuid": "1234-ABCD", "sizeBytes": 62008590336, "mountpoint": "/media/eirl-rec",
 "dir": "/media/eirl-rec/EnhancedIRL", "readOnly": false, "dirty": false, "writeMBps": 31.2,
 "at": "2026-10-24T12:41:03+02:00"}
```

| `state` | Meaning | `reason` |
|---|---|---|
| `none` | No disk | — |
| `checking` | Mounting, checking and measuring | — |
| `ready` | Mounted and writable: the engine may record | — |
| `unsupported` | A disk the encoder does not use | `fs_ntfs`, `fs_unknown`, `too_small` |
| `unformatted` | No filesystem at all: it can be formatted | — |
| `error` | It could not be used | `mount_failed`, `readonly`, `check_failed` |
| `ejected` | Unmounted by `eject`: it can be pulled out | — |

`seq` goes up on every change of the mounted disk's state, so that a disk pulled out and plugged back in is a new
mount for the engine; a second disk coming or going only changes `anotherDisk`, with the same `seq` (a new one would
stop a recording as `disk_removed`: found reading the code on Oct 28, never seen on the board). tmpfiles
writes `{"state": "none", "reason": "", "seq": 0}` at boot. `fs` is `exfat`, `fat32` (or `fat16`/`fat12`, mounted the
same way: the engine splits files for any `fat*`) or `ext4`; for an unsupported disk, `ntfs` or blkid's type.
`sizeBytes` is the mounted partition's size (the disk's otherwise); `readOnly` and `dirty` appear once a filesystem was
chosen. `check_failed` covers an `EnhancedIRL` that is a file or a link, an ext4 root the engine cannot enter, and a
write test that failed or took more than 60 s. Fields that do not apply are left out.

## 5. The engine

### 5.1 Configuration

The app's file (`engine.env`, in the engine's whitelist): `REC_MODE` (`off` · `manual` · `with_output`, default `off`)
and `REC_QUALITY` (`standard` · `high` · `max` · `efficient`, default `high`). The board's settings
(`/etc/default/eirl-engine`, root): `REC_DIR` (default `/media/eirl-rec`) and `REC_STORAGE_STATE` (default
`/run/eirl-storage/state.json`). No key the app writes is a path. `output_config` rereads `REC_*` live.

### 5.2 Socket and state

- Command `{"cmd": "record", "on": true}`. Errors: `unsupported` (no recorder), `disabled` (`REC_MODE=off`),
  `no_disk` (the storage state is not `ready`), `disk_full` (less room than a clean stop needs), `bad_request`.
- In `with_output`, `output on` also starts recording (when the disk is ready) and `output off` stops a recording
  that `output on` started; a recording started with `record` only stops with `record`.
- The state gains `record`: `mode` (`on` · `off`), `state` (`off` · `starting` · `on` · `error`), `setting` and
  `quality` (`REC_MODE` and `REC_QUALITY` as the engine has them now, after `output_config`), `reason`, `detail`,
  `by` and `seq` (as `output`), and while on `elapsedS`, `kbps`, `droppedS`, `file` (relative to the disk); and `disk`:
  the storage state's `state`, `reason`, `fs`, `label`, `sizeBytes`, plus `freeBytes` and `minutesLeft` at the
  quality's bitrate.
- Reasons: `no_disk`, `disk_removed`, `disk_full`, `disk_slow`, `write_error`, `encoder_error`, `power_lost`, `internal`.

### 5.3 Files

`EnhancedIRL/2026-10-24_1530.mp4` (the board's local time) when the clock is synchronised, `EnhancedIRL/rec-0042.mp4`
(a counter kept in `STATE_DIR`) when it is not. A name already taken gets `-2`, `-3`… On FAT32 the next file starts at
3.9 GB: `…_1530-2.mp4`, at the next video fragment that starts with a keyframe, with the same header (ftyp and moov) as the first.

### 5.4 The writer

- It splits what the muxer sends into top-level boxes. The header (`ftyp`, `moov`) is kept for a FAT32 split.
- A buffer of 64 MB between the muxer and the disk. When it is full it drops whole fragments (`moof` + `mdat`), never
  part of one, counts the seconds dropped and reports `disk_slow`; the file stays valid, with a jump. It resumes at a
  video fragment whose first sample is a keyframe (the trun's or tfhd's sample flags): when the encoder's keyframe
  comes a frame or two after the muxer's second, mp4mux first closes a fragment of just those frames (§8.2).
- `fsync` after every fragment: a power cut loses ~2 s.
- Below 64 MB plus 30 s at the quality's bitrate the recording finishes its file and stops: `disk_full` (D7). With
  less than that already, no recording starts and no file is made (REC answers `disk_full`; Go live records nothing).
  `ENOSPC` → `disk_full` too; `EIO`, a vanished mount or a new storage `seq` → `disk_removed` (and, if REC is still on
  when a disk is ready again, a new file).
- A marker in `STATE_DIR` (`recording.json`: the file and its storage `uuid`) while recording, removed on a clean stop.
  If the engine starts with it, the file is cut at its last complete fragment (when that disk is mounted) and the
  state says `power_lost`. It also stays after a `disk_removed`, so a file torn by pulling the disk out is cut when
  that disk comes back. A recording started on another disk first replaces it: that file is then left as it is.
- The file's timeline starts near 0 (0.2 to 0.45 s, measured). The program's timestamps start wherever the program
  is (minutes or hours in), so before the pipeline plays both appsrcs' src pads get the same offset: minus the
  program's running time, less 250 ms for the buffers already on their way (`gst.Pipeline.ZeroStart`; pad offsets,
  because the buffers are the program's, shared with the stream). It must be in place before any data: the first
  version set it on the first video buffer, the segment could then go out twice, and with two segments the video
  reached the file about once every offset's length (§8.3). With no offset at all the first file of the board test
  started at 33.9 s (QuickTime hid it, ffmpeg did not).
- `mfra` (the muxer's index at the end) is never written: its offsets would not survive a dropped fragment or a split,
  and a fragmented MP4 does not need it. Nor is the `moov` mp4mux sends again after it, with the durations filled in:
  it is meant to go over the first one in place, and appended the file would have two (§8.2).
- A pipeline that does not start leaves no empty file and no marker; `encoder_error` and `write_error` are retried
  after 2, 4, 8, 15 and 30 s, each time in a new file. `disk_full` stops (D7) and stays in the state until the next
  recording or another disk (a new storage `seq`).

### 5.5 Busy

Recording counts as busy wherever streaming does: the OTA (`update.Streaming`), the USB hub's power cut, the HDMI's
replug pulse, the slots, `tools/deploy-engine.sh`. A capture card that hangs while recording is not power-cycled
(the cut would switch the disk off with the whole hub): its detail is `hung_recording` (ENGINE_GO.md §13).

### 5.6 Code

`manager/internal/engine/record` (`recorder.go` the state machine and sessions, `writer.go` the queue and the file,
`mp4.go` the boxes and the cut after a power loss, `storage.go` the helper's state, `names.go`), `pipeline.Recording`
and the program's `recv`/`reca` (with the converter's pool at 16 instead of 12: a session holds program buffers until
its own converter copies them), `config/reccfg.go`, and in the engine `startRecorder`, `setRecord`,
`recordWithOutput`, `busy`. `gst.Pipeline.SendEOS` closes the MP4's last fragments on a clean stop.

## 6. The app and the web (R3)

The contract between the app (`manager/internal/...`) and the web (`manager/web/static/`). Everything is additive:
with an older engine `status.mixer.record` is absent and the web hides the recording; an engine without a recorder
answers `unsupported`.

### 6.1 Settings

`REC_MODE` (`off` · `manual` · `with_output`, default `off`) and `REC_QUALITY` (`standard` · `high` · `max` ·
`efficient`, default `high`) are app settings in `engine.env`, validated against those closed lists. They are applied
hot with `output_config`, never with a restart: the engine gives them to the recorder and leaves the stream alone
(`config.SameOutput`). Recording itself is never saved: after a power cut it stays stopped (D6).

### 6.2 Routes

Session and CSRF for the mutations, like the rest of `/api/v1`, and the same routes on the local socket for the
screen (R4). Errors are `{"error": code, "message": text}` in the request's language.

| Route | Body | Answer |
|---|---|---|
| `GET /recording` | — | 200 the recording view (below) |
| `PUT /recording` | `{"mode"?, "quality"?}`, at least one | 200 the view, after `output_config`. 400 field errors; 409 `recording`: the quality cannot change while recording (the mode can; `off` stops the recording) |
| `PUT /record` | `{"on": bool}` | the engine's `record` command; 200 the view. 409 `disabled`, `no_disk`, `disk_full`, `unsupported`; 503 when the engine does not answer, as for the other engine commands |
| `GET /storage` | — | 200 the storage object |
| `POST /storage/eject` | — | `sudo -n eirl-storage eject`; 200 the storage object. 409 `no_disk` (exit 2), `recording` (3), `busy` (4); 500 `eject_failed` (1, other) |
| `POST /storage/format` | `{"seq": n}` | 409 `disk_changed` unless the storage state's `seq` is still `n` and a disk is there (not `none`, `checking`); then `sudo -n eirl-storage format` (up to 120 s); 200 the storage object. 409 `no_disk`, `recording`, `busy`; 500 `format_failed` |

The recording view:

```json
{"mode": "manual", "quality": "high",
 "qualities": [{"id": "standard", "codec": "h264", "videoKbps": 15000, "audioKbps": 192},
               {"id": "high", "codec": "h264", "videoKbps": 25000, "audioKbps": 256},
               {"id": "max", "codec": "h264", "videoKbps": 40000, "audioKbps": 256},
               {"id": "efficient", "codec": "h265", "videoKbps": 20000, "audioKbps": 256}],
 "record": {"mode": "off", "state": "off"},
 "storage": {"state": "ready", "seq": 7}}
```

`record` is the engine's object as in the state (§6.3), `null` with an older engine; `storage` is the storage object.

The storage object is the helper's state file (§4.3) with a closed set of fields: `state`, `reason`, `seq`,
`device`, `model`, `fs`, `label`, `sizeBytes`, `readOnly`, `dirty`, `writeMBps`, `anotherDisk`. No paths. Without
the file, `{"state": "none", "seq": 0}`; a state outside §4.3's list reads as `none`.

### 6.3 State

`status.mixer.record` (and the SSE's mixer state) forwards the engine's object (§5.2) with closed lists, like
`output`: `mode` (`on` · `off`), `state` (`off` · `starting` · `on` · `error`), `setting` and `quality` (closed lists,
absent with an engine before them), `reason` (§5.2's list), `detail`
(≤ 200), `by` (`start` · `socket` · `screen` · `output`), `seq`, `elapsedS`, `kbps`, `droppedS`, `file` (a name,
no folder), and `disk`: `state`, `reason`, `fs`, `label`, `sizeBytes`, `freeBytes`, `minutesLeft`, `dirty`. The web
fetches `GET /recording` again when `record.disk.state` changes (a disk plugged in or out). The engine command
`record` (`on`) is also allowed through `POST /mixer`, like `output`.

### 6.4 Updates

While recording, the OTA's guards answer with their own code: `POST /update/install` and `/update/reboot` 409
`recording`, and `GET /update` `blocked: "recording"`, each with a message that says to stop the recording (today
they say to stop streaming).

### 6.5 The web

A card **Recording** (`#recording-card`) in the configuration zone, after the destination's, hidden without
`status.mixer.record`:

- Its state: off; `● REC 01:12:34 · 3 h 20 min left`; or the error's reason in words (`power_lost`: "Recording
  stopped because of a power cut"; `disk_removed` in red).
- REC (start/stop), disabled with the reason: the mode is off, no disk ("Connect a USB disk"), the disk is full.
- The mode (Off · Independent · With the stream, each with one line on what it does) and the quality (Standard
  15 Mbps · 6.8 GB/h, High 25 Mbps · 11.4 GB/h, Maximum 40 Mbps · 18.1 GB/h, Efficient H.265 20 Mbps · 9.1 GB/h:
  video and audio); the
  quality disabled while recording.
- The disk: its name (label, else model), filesystem, size, free space and time left, the write speed. Warnings:
  FAT32 (files split every 3.9 GB), `dirty` (not ejected last time: check it on a computer), read-only, NTFS or
  unknown (cannot be used: format it), unformatted, another disk connected (only one is used), under 10 min left.
- Eject (with a disk ready and not recording) and Format (with a disk and not recording), Format behind a
  confirmation that names the disk and says everything on it is erased.
- With `with_output` and a disk ready, Go live says "Go live + record".
- Texts in `lang-es.js`, `lang-en.js` and `lang-fr.js`; tried at 390 and 1280 px against `eirl-encoder-sim`.

### 6.6 As built (Oct 28)

Where the code settles what §6.1-§6.5 left open:

- The OTA says `recording` only when recording without streaming: while streaming, with or without a recording, it
  stays `streaming` (with `with_output`, Stop stops both). `GET /update` then has `streaming: false` and
  `blocked: "recording"`. When the update helper itself refuses because of a recording, its result still says
  `streaming`: it would need a code of the updater's own.
- `PUT /record`: 503 `mixer_disabled`, `mixer_off`, `mixer_error` or `timeout` when the engine does not answer;
  `unknown_command` (an older engine) is 409 `unsupported`; any other engine code 422.
- `PUT /recording` never restarts the engine: one that is stopped, older or waiting for a restart takes the settings at
  its next start. The quality's 409 is only for a real change while recording or starting.
- The storage object leaves out an empty reason, `uuid` and the paths; model and label are cleaned like a device's
  name (≤ 48 characters).
- Eject is waited for 90 s and format 120 s; a format that runs past it answers 500 `format_failed`, and the helper's
  unit may still finish it (the state file says how).
- The web hides Format for a disk under 1 GB and for a write-protected one, shows "Go live + record" only while not
  recording, blocks Install and Restart while recording itself (before the app's 409), and tells an ejected disk to be
  unplugged and plugged in again.
- The web reads the mode and quality from `GET /recording` when the card shows, after its own actions, on a disk's
  change, when the page comes back to the front, and when the engine's `record.setting` or `record.quality` changes:
  a change made on the screen or in another browser shows at once.

### 6.7 The screen (R4, Oct 28)

The same as the web, on the 1024×600 screen (MIXER.md §23):

- The header's second line: `● REC 01:12:34 · 3 h 20 min left` while recording (amber under 10 min, or that the disk
  cannot keep up); why a recording stopped on its own for 5 min (`power_lost` at start, `disk_removed` in red), longer
  while REC is still on and waiting. Settings › Recording keeps the reason.
- REC at Go live's right, a third of its row, held 1 s like Go live; hidden with `REC_MODE=off`; disabled with the
  reason without a disk or with a full one; the engine's refusals in words. With `with_output` Go live says "Go live +
  record" with a disk ready, and "no disk: not recording" under it without one.
- A disk plugged in while live: an amber band over the program for 15 s (a hard disk only through a powered hub).
- Settings › Broadcast › **Recording**, a fourth tab (eight entries do not fit the Settings list): mode and quality
  pickers, the disk and its warnings, Eject and Format. Format, and turning the mode off while recording, ask inside
  the page with Cancel and a red "Erase and format", without the screen's 3 s second tap (formatting cannot be
  undone). The app's `GET /recording` every 2 s while the page is open.
- French shortens Go live's label to «DIFFUSER + ENREG.».

The simulator (`cmd/eirl-encoder-sim`) gets what the web needs: the `record` command and state (elapsed time,
bitrate, free space going down), `with_output` on Go live, and a storage helper (its state file and eject/format)
with a disk that can be plugged and pulled.

## 7. Phases

| Phase | What | State |
|---|---|---|
| R0 | Measurements on the board | done (§3) |
| R1 | The storage helper, udev, units, packages | code and tests done (Oct 24); first board test Oct 24 (§8.1) |
| R2 | The engine: recorder, writer, socket, state, busy | code and tests done (Oct 24); first board test Oct 24 (§8.1) |
| R3 | The app and the web: keys, API, card, translations | done (Oct 28, §6), tried in the simulator and on the board; released in 0.12.0 |
| R4 | The screen: header indicator, REC button, Settings › Recording | done (Oct 28, §6.7), seen in captures and on the board; released in 0.12.0 |
| R5 | Robustness on the board | nearly done (Oct 24 and 28, §8.1-§8.3): a cable power cut and the owner's editors left |
| R6 | Release, and the cloud | released in 0.12.0 on Oct 28: a security review of the helper first (§4.4), then the update tried on the test board from 0.11.1 (test builds, the system layer with the five packages, ~1 min, no restart), and the test board updated itself from GitHub. The cloud (D8) still to do |

## 8. To test on the board (R5)

Each needs a slot agreed with the owner. Plug disks in **before** going live: two power cuts in R0 (§3).

1. Install (`install.sh` and the layer): `/bin/fusermount` 4755, no initrd regenerated (`ls -l /boot`), the state file
   `none`.
2. The exFAT pendrive: `checking` → `ready`; `findmnt /media/eirl-rec` with `nosuid,nodev,noexec`; `mount.exfat` in
   `systemctl status eirl-storage@sda`; a plausible `writeMBps`; the engine's user writes in `EnhancedIRL/`, the app's
   reads.
3. Pulled out without ejecting, also while `checking`: the unit stops, a lazy unmount, `none` with a new `seq`, no
   mount or FUSE process left.
4. `sudo -n eirl-storage eject` as the app: `ejected`, the unmount seen by the engine; exit 3 while recording, for
   eject and format.
5. Format a FAT32, an NTFS and a blank stick; a whole-disk FAT32 stick; ext4 (`EnhancedIRL` 0750
   `eirl-engine:eirl-encoder`); a write-protected SD (`error/readonly`); a second disk (`anotherDisk`, then the
   takeover); a card reader plugged in empty; a reboot with a disk plugged in.
6. The engine: `REC_MODE=manual` in `/etc/default/eirl-engine`, `eirl-enginectl record on|off`: the program does not
   drop below its rate with the converter's pool at 16; the file opens in VLC, DaVinci Resolve, Premiere and
   QuickTime; H.265 (`efficient`) as `hvc1` in QuickTime; the engine's CPU while recording.
7. `REC_MODE=with_output`: Go live from the screen starts both, Stop stops both; 4 h streaming and recording at
   1080p60 (RAM flat, 0 s dropped); the link cut for 5 min (no gap in the file).
8. Failures: pull the disk out while recording (`disk_removed`, the stream goes on, the file cut when it is back);
   fill a small stick (`disk_full`, a clean file); a FAT32 stick for over 21 min (files that chain); a power cut while
   recording (`power_lost`, the file cut and opening in QuickTime).
9. Busy: an OTA install and `tools/deploy-engine.sh` refused while recording; a hung capture card not power-cycled
   (`hung_recording`).

### 8.1 First board test (Oct 24, 17:10-17:48)

The branch's app and engine, and the five packages installed by hand from `system/packages.lock` (checked against
their sha256). `REC_MODE=manual` in `/etc/default/eirl-engine`; the record command over the socket (`eirl-enginectl
record` came after).

| # | Result |
|---|---|
| 1 | `/boot` unchanged, `/bin/fusermount` 4755, the state file `none` |
| 2 | Pendrive (Kingston DataTraveler 3.0, exFAT): `checking` → `ready` in ~1 s, `fuseblk` with `nosuid,nodev,noexec,noatime`, `mount.exfat` in the unit's cgroup, 56.5 and 61.4 MB/s at the mount test |
| 3 | The 2.5" disk pulled out: `detach present=false`, `none` with a new `seq`, no unit, mount or FUSE process left |
| 5 | An NTFS disk: `unsupported/fs_ntfs`, not mounted. A reboot with both plugged in: the pendrive `ready`, the NTFS disk `anotherDisk`. When the pendrive was pulled, the helper took the other disk over |
| 6 | Recording only: 60 fps in and encoded, program at 60 fps, 0 s dropped, 187 MB for 66 s (22.7 Mbps of the 25 VBR). It decodes whole and plays in QuickTime (AVFoundation: 65.1 s from 0). Its timeline started at 33.9 s: fixed since (§5.4) |
| 6 | Recording and streaming (SRTLA H.265 to the test destination): both at 60 fps, the stream at ~6.6 Mbps, the recording at ~25 Mbps; the engine at 90-99 % of one core (72 % streaming only, §3), the board's RAM at 1.44 GB |
| 8 | The pendrive pulled out while recording and streaming: `record_failed reason=disk_removed` from the fsync's EIO, the stream untouched |
| 8 | Then a power cut (below): the engine came back with the recording off, `power_lost` and its file, and the marker kept for the cut. When the pendrive was back (18:33), `power_lost_trimmed` cut 6.4 MB off the end and removed the marker: the file decodes whole and plays in QuickTime (2 min 24 s) |
| 6 | The timeline fix (§5.4) on the board: video from 0.033 s with a keyframe, audio from 0.0007 s (that first version is replaced since: §8.3) |
| 7 | `REC_MODE=with_output`: Go live (over the socket) started the recording too, `by=output` |

🔴 **Two more power cuts, both from the 2.5" disk without its own power** (R0 had two, §3): plugging it into the
unpowered external hub with the board idle; and when the pendrive was pulled out while recording and streaming, the
helper took the waiting disk over and woke it up. That second one is ours: a waiting bus-powered hard disk is woken
at any moment. The owner chose to keep the takeover (Oct 24): hard disks only with a powered hub. The external hub
(self-powered by its descriptor, with no supply) did not detect the pendrive in any port.

### 8.2 Four hours streaming and recording (Oct 24, 18:34-22:34)

The engine with the timeline fix, `REC_MODE=with_output`, the pendrive in a port of the board. Go live over the socket
started both; SRTLA H.265 to the test destination; stopped by the script after 240 min.

| What | Result |
|---|---|
| Program | 60 fps all along (59-60.3 measured each minute) |
| Stream | on for the whole run, ~6.6 Mbps; only the link's NAK bursts in the log |
| Recording | 0 s dropped in 239 one-minute samples; 25.5 Mbps (45.9 GB for 14 422 s) |
| The file | One file (exFAT): 29 139 fragments numbered without a gap; video from 0.033 s to 14 422.4 s, fragments every 1 s, audio every 0.981 s; its length is the time from `record_started` to `record_stopped`. The header and the last 4 fragments decode (ffmpeg) and play (AVFoundation) |
| Engine CPU | 94 % of one core on average (103 % at most); the board at 143 of 400 % |
| Engine memory | Goroutines (39) and threads (130) flat; Go heap 20-29 MB. RSS by hour 128, 127, 135, 141, 145 MB (+5.8 MB/h overall: flat for two hours, then rising). Four hours do not settle it: a longer run, and one streaming only to compare, are to do |

Found in the file, both fixed in the writer since (unit tests; not yet run on the board):

- **A second `moov` at the end.** On a clean stop mp4mux sends the `moov` again (the same 1337 bytes, now with the
  durations and modification times), meant to go over the first one; the writer appended it. ISO BMFF allows one.
  Now left out (§5.4).
- **Video fragments that do not start with a keyframe: 19 of 14 442**, each one 1 or 2 frames long, right before a
  keyframe that came late against the muxer's second. A FAT32 split or a resume after a drop at one of them would
  start with frames nobody can decode. Both now wait for a fragment that starts with a keyframe (§5.4).

### 8.3 The rest of R5 (Oct 28, 17:40-19:15)

The branch's app, helper and engine with the fixes of §8.2, the Kingston pendrive (the owner agreed to wipe it) in a
port of the board. Remaking the stick by hand on the board (`wipefs`, `sfdisk`, `mkfs`): `sfdisk`'s change event
starts the disk's unit, which looks before `mkfs` is done; restart the unit afterwards. Only a test matter.

| # | Result |
|---|---|
| 6 | A clean stop now leaves one `moov` (§8.2 fix) |
| 4 | `eirl-storage eject` and `format` as the app while recording: exit 3, `event=refused … reason=recording` |
| 9 | While recording the updater does not download, and `POST /update/install` answers 409 `streaming` (its message speaks of streaming only: for R3). `tools/deploy-engine.sh` refuses (`STREAMING: not installing`) and the engine is untouched |
| 7 | `with_output`, live and recording, the relay's IP blocked 5 min (iptables, lifted by a timer): the stream retried every 10 s (`relay_unreachable`) and was back ~50 s after the block went; the recording went on in one file, 517 s, 1047 fragments without a gap, 0 s dropped. Stop stopped both |
| 5 | Blank stick: `unformatted`, REC → `no_disk`. `eirl-storage format` as the app: 1.7 s, one MBR partition of type 7, exFAT `EIRL-REC`, `ready`, 63 MB/s |
| 5 | ext4 (a 1.2 GB partition): `ready`, the mount's options, `EnhancedIRL` owned by the engine, `lost+found` left alone; 14 MB/s at the mount test (a new ext4 initialising its tables) |
| 8 | Disk full on it: the recording stopped at 152 MB free (64 MB plus 30 s), `disk_full`, a clean 308 s file |
| 5 | FAT32 (one partition): `ready`, `uid`/`gid`/`fmask`/`dmask` in the mount, 67 MB/s |
| 8 | FAT32 at `max` (40 Mbps): the first file closed at 3.90 GB (13 min), the recording went on in `…_1835-2.mp4` with the same header and a keyframe first; video 0.34-776.34 s then 777.34-852.34 s, audio as continuous, 0 s dropped. The second file decodes and plays (AVFoundation, 76 s; its timeline starts at 777 s, which players hide) |
| 6 | `efficient`: H.265 Main as `hvc1`, 1080p60, plays in AVFoundation |
| 5 | FAT32 on the whole disk (no partition table): mounted from `sda`, records. `eirl-storage format` from it: exFAT `EIRL-REC` again |
| — | The board back on 0.11.1 at 18:55 (§8.2's way); the stick left exFAT and empty |

Not done: a power cut by pulling the cable (the owner chose another day; the cut of Oct 24 covered `power_lost` and
the trim), a write-protected SD and an empty card reader (no such hardware), `hung_recording` (no way to hang a card
on purpose; unit tests only), opening the files in VLC, DaVinci Resolve and Premiere (the owner's).

Found and fixed (unit tests, then checked on the board):

- **REC on a full disk started anyway** and left a 0.8 MB file at each press. Now refused before any file: the
  command answers `disk_full` (MIXER.md), and Go live records nothing (§5.4).
- **On ext4 the files were `eirl-engine:eirl-engine`**: the app could not read them. The folder is now setgid
  (§4.2): files `eirl-engine:eirl-encoder`, read by the app's user.
- **The encoder let ~1 frame a second through in 3 of 9 recordings**: 60 frames a second reached the recording,
  ~1 left the encoder, on exFAT and FAT32, at any quality. In those files the video started at 14.9 s and 3.9 s
  (each time the program's age when the recording began) and then came in bursts every 15 s and every 4 s; the audio
  was whole. The first ZeroStart set the pad offset on the first video buffer, after the appsrc had sent its segment,
  so the segment went out a second time, with the offset, whenever the queue had pushed the first one already: a
  race, and the likely cause (inferred, not seen in a trace). Now the offset goes on before the pipeline plays (§5.4): 10 recordings in a row, half right after an engine
  restart, all at 60 fps, video from 0.25-0.44 s with a keyframe, audio from 0.17-0.37 s.
