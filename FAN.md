> **Status:** IN PROGRESS (step 1, one curve, on the test board since Oct 15; the second fan, on and off, and its switch in Settings, released in 0.17.0; profiles and the "camera" mode are next)
> **Updated:** 2026-11-06

# Fan control

The encoder drives the Jetson Nano's fan itself: `eirl-fan`, a small root daemon, sets the fan's speed from the
chip's temperature with a curve of ours. It replaces the kernel's own control, which leaves the fan stopped until
about 51 °C and then moves it in coarse steps, and the test board's old hand-installed `automagic-fan` (a Python
loop that ran the fan almost at full speed all the time). It is the first step of IMAGE.md's decision D-I7: thermal
profiles chosen in Settings and a mode like a professional camera's (much more fan than needed while not on the
air, quieter while streaming) come on top of it (§6). On a board with a second fan, it also switches that one on
and off (§5).

## 1. What it does

- Every 2 s it reads the hottest of the chip's `CPU-therm` and `GPU-therm` zones (`/sys/class/thermal`), the parts
  that throttle (at about 100 °C). `AO-therm` reads some 8 °C above them and is not where the heat is made.
- It sets `/sys/devices/pwm-fan/target_pwm` (0-255) from the curve (§2), only when the value changes. The
  pwm-fan driver ramps to it by itself.
- **It speeds up at once and slows down late**: a lower speed only when the temperature is 3 °C below what the
  current one was set for. A temperature hovering around a point (the sensors move in 0.5 °C steps) does not make
  the fan hunt, and it stops 3 °C below where it starts.
- **Safety**: at 80 °C or above, full speed whatever the curve says; a reading it cannot make (no zone, a value out
  of range), full speed.
- **The kernel's control is never left off without a controller**: the daemon switches the kernel's control off
  (`temp_control` = 0) when it starts and back on when it stops; if it dies, the unit's `ExecStopPost` gives it
  back, and systemd starts the daemon again 2 s later. At boot the driver starts with the kernel's control on.
  It also gives the fan back and exits when it cannot set the speed 5 times in a row (systemd tries again), and when
  its own binary is replaced or removed: an update restarts it with the new one, and a rollback to a release
  without it (the unit gone too) leaves the kernel in charge.
- State for the app and for people: `/run/eirl-fan/state.json` (`pwm`, `tempC`, `sensor`, `sensors`, `curve`,
  `error`, `at`, and `fan2` on a board with the second fan, §5). The journal gets a line when the fan starts, stops
  or reaches full speed, and one per hour with that hour's temperatures and speeds: `journalctl -u eirl-fan`.
- `eirl-fan -once`: one reading, printed, touching nothing.

Code: `manager/internal/fan` (the curve and the controller, with tests) and `manager/cmd/eirl-fan` (the daemon).
Unit: `manager/deploy/eirl-fan.service`: root, because the fan's speed is a root file in sysfs, but with no
capabilities and no network, and a read-only system, `/sys` and `/proc/sys` included, except its state directory, the
fan's directory in sysfs and, on a board with the second fan, that fan's pin (§5).

## 2. The curve

| Hottest of CPU and GPU | Speed (PWM of 255) |
|---|---|
| below 38 °C | stopped (from 35 °C down, once running) |
| 38 °C | 80, where the fan surely spins (the driver's own table puts 1000 rpm there) |
| 50 °C | 140 |
| 58 °C | 200 |
| 65 °C and above | 255, full speed |

Linear between the points. Measured on the test board on Oct 15, idle (the engine running, not streaming), with the
fan at a fixed speed for 3 minutes each:

| Fan | CPU | GPU |
|---|---|---|
| 80 (what the kernel's control had set: the zone was just above its 51 °C step) | 45.5 °C | 44.5 °C |
| 130 | 36.5 °C | 35.5 °C |
| 255 | 36 °C and still going down | 37.5 °C |

Most of the cooling is between 80 and 130. With the curve, the idle board settles at 40 °C with the fan at about 98 (§3),
and the fan follows the load up from there.

## 3. Results on the test board

Oct 15, installed by hand from this branch: the daemon takes the fan from the kernel, its sandbox can write the
speed, stopping it gives the fan back to the kernel, and after a `kill -9` `ExecStopPost` gives it back and systemd
restarts it 2 s later. Idle (the engine running, not streaming), it settles at 40 °C with the fan at about 98, from
the kernel's 45.5 °C at 80. Its cost: 0.18 s of CPU in 5 minutes (0.06 % of a core) and 6 MB of memory. Under a
stream: to be measured.

## 4. Install and undo

`install.sh` installs `/usr/local/sbin/eirl-fan` and `/etc/systemd/system/eirl-fan.service`, enables it and
(re)starts it; in an image (`EIRL_IMAGE=1`) it only enables it. It arrives by OTA like the rest of the package. The
unit conflicts with the old `automagic-fan.service`, and `install.sh` disables that one if it is there (back:
`sudo systemctl enable --now automagic-fan` after disabling `eirl-fan`).

The board's settings, `/etc/default/eirl-fan` (the second fan's, §5), are not installed by `install.sh`: the file is
written by hand on a board with the second fan, and an update leaves it as it is.

Undo: `sudo systemctl disable --now eirl-fan` (the kernel's control comes back at once), then delete
`/usr/local/sbin/eirl-fan` and `/etc/systemd/system/eirl-fan.service`. A second fan is left as it is (§5).

## 5. The second fan

Some boards have a second fan, a small 5 V one that is only on or off, on pin 32 of the 40-pin header (`gpio168`). How
it is wired, and why it has no speed control, is in [`HARDWARE.md` §6](HARDWARE.md#6-the-second-fan). The daemon
switches it from the same reading as the main fan, the hottest of CPU and GPU:

- **On at 55 °C, off at 50 °C, and never on for less than 2 minutes.** Off idle (40-46 °C) and in an ordinary stream
  (49.5-53 °C, measured on Nov 4, with the main fan at 150-163); on from where the main fan is at about 70 % (178 at
  55 °C). The minimum time keeps a temperature hovering around a threshold from clicking it on and off; it never
  delays switching it on.
- **Safety**: on at once at 80 °C or above, or with a reading it cannot make, whatever the thresholds say.
- **Left as it is when the daemon stops or dies** (a restart, an update, a shutdown): the main fan goes back to the
  kernel's control, which keeps the chip safe on its own, and the next start takes the second fan from where it is.
  A fan found on stays on for the minimum time, so a restart never switches it off and on again. Switching it on at
  every stop was tried first and dropped: every restart, update and shutdown was loud for no need. A pin changed by
  someone else is set again, with a line in the journal.
- **Switched off in Settings** (below), it only comes on for the safety: at 80 °C, off again at 75 °C after the
  minimum time, or with a reading it cannot make. Until the next restart.
- State: `fan2` in `state.json` (`on`, `since`, `reason` while on: `temp`, `hot`, `reading`, or `start` when it was
  found on; `mode`, `auto` or `off`; `onC`, `offC`, `hotC`, `minOnS`, `gpio`, and `error`, `configError` or
  `controlError` when there is one). The journal gets a line each time it is switched (`second fan on`, with the
  temperature and the reason; `second fan off`, with how long it was on) or switched off and back in Settings, and the
  hourly line adds `fan2OnPct` and `fan2Switches`. `eirl-fan -once` prints it too.

**In Settings** (since Nov 6): the screen's System › Fan and the web's «Fan» card show the temperature and the main
fan's speed and, on a board with the second fan, its switch: **automatic**, or **off until the next restart**, to
silence it when it matters (a quiet scene), at the user's risk: the board runs hotter and under heavy load it may slow
down. Switching it off asks first (the screen, a second tap; the web, a question) and says so; back to automatic, at
once. Both go through `GET`/`PUT /device` (`fan`, SETUP.md §2):

- The app writes the request to `/etc/eirl-encoder/fan.json`: `{"fan2":"off","bootId":"…"}`, with this boot's id
  (`/proc/sys/kernel/random/boot_id`), or `{"fan2":"auto"}`. It is `0644`, the only one of the app's files that is:
  the daemon's root has no capabilities and reads it as "other" (MANAGER.md §5); there is nothing secret in it.
- The daemon rereads it at every reading (2 s) as data: only a regular file of the app's user or of root
  (`safefile.ReadFrom`, no links), a known mode, and `off` only with this boot's id. The next boot starts automatic
  without anyone deleting the file. Anything odd: automatic, logged once, and in `controlError`.
- Off stops it at once, without waiting for the minimum time: it was asked for. Back to automatic, the next reading
  applies the thresholds.
- `GET /device` reads the fan daemon's `state.json` (`fan` is there while the daemon runs) and the request (the mode
  shows at once, before the daemon's next reading).

**Settings**: `/etc/default/eirl-fan`, optional, read by the unit (§4); `sudo systemctl restart eirl-fan` applies them.

| Variable | Default | Meaning |
|---|---|---|
| `EIRL_FAN2` | off | `on` on a board with the second fan. Without it, the daemon and the unit never touch pin 32 |
| `EIRL_FAN2_ON_C` | 55 | On at or above it, °C |
| `EIRL_FAN2_OFF_C` | 50 | Off at or below it, °C, once on for the minimum time |
| `EIRL_FAN2_MIN_ON` | 2m | The minimum time on (`90s`, `2m`; up to `30m`) |

A value it cannot read, or thresholds out of order (off must be below on, and on below 80 °C), mean the defaults,
with the reason in the journal and in `configError`.

**The pin, through sysfs and not the GPIO character device.** A line taken through the character device is released
when the process exits, and the pad goes back to its special function, as with an `unexport`: the fan would be left
at whatever level the header's level shifter keeps (HARDWARE.md §1). A sysfs export lives in the kernel, so the pin
stays an output, at its last level, whatever happens to the daemon. It is never unexported.

- The unit's `ExecStartPre` runs `eirl-fan -claim` outside the sandbox. It exports the pin and makes it an output at
  the level the temperature asks for, writing `high` or `low` to `direction` (level and direction at once, no
  glitch). A pin that is already an output is left as it is. A failure there is logged and the main fan is still
  driven. The daemon starts about 4 s after the kernel; until then the pin is wherever the level shifter left it.
- The sandbox: `/sys` and `/proc/sys` are read-only except `/sys/devices/pwm-fan` and the pin's own directory, so the
  daemon cannot export, unexport or set any other pin. It is `ReadOnlyPaths`, not `ProtectKernelTunables`: systemd 237
  (L4T's) applies that one to the `+` commands too, and the export needs a writable `/sys`.

**Results on the test board** (Nov 5, installed by hand from this branch, the engine running, not streaming):

- The sandbox, from inside the unit's namespace: `/sys/class/gpio/export`, `unexport` and `/proc/sys` are read-only;
  the main fan's files and the pin's `value` can be written; `/dev` is private.
- The claim at a start: pin 32 exported and made an output, off at 38 °C.
- A real crossing, 4 busy CPU loops for 6 minutes from 51.5 °C: on at 55 °C after 35 s (the main fan at 178). With
  both fans the CPU stayed at 56-57 °C (the main fan at 189-193). Off at 50 °C 50 s after the load ended, 6 min 10 s
  later.
- By ear, as it has no tachometer:
  - **Forced on** (thresholds 30/25 °C and a restart): it is on. Idle, at 48-50 °C, it hardly lowers the temperature,
    but the main fan runs at 133-144 instead of about 151.
  - **Back to the defaults and a restart**: it stays on with no gap, and it is off 2 minutes later (`onFor=2m0s`), at
    47.5 °C.
  - **A restart and a `kill -9` of the daemon while it is off** (Nov 6): it stays off, silent; after the kill the
    unit's `ExecStopPost` gives the main fan back to the kernel, and the daemon is back 2 s later.
  - **A reboot with the pin left on, and nothing driving it** (an update to a release without the second fan): the
    level shifter keeps it on through the reboot. Back on this branch's daemon, the claim makes the pin an output and
    switches the fan off (42 °C).
- Three reboots (not cold): the claim takes the pin 4.7 to 5.2 s after the kernel starts, off at 39-40.5 °C.
- A cold boot (the power removed, the clock back to 2000): the claim takes the pin 3.9 s after the kernel starts, off
  at 39.5 °C. By ear, it was silent all the way through: that time the pin came up off.

**Undo** on a board: delete `/etc/default/eirl-fan` (or set `EIRL_FAN2=off`) and restart `eirl-fan`. The pin stays an
output at its last level; to stop the fan, `sudo sh -c 'echo 0 > /sys/class/gpio/gpio168/value'`.

## 6. Next

- **Profiles**: the app writes the chosen one in `/etc/eirl-encoder` and the daemon reads it as data (a name from a
  fixed list), as the second fan's switch already does (§5); System › Fan and the web's card grow a choice.
- **"Camera" mode**: from `/run/eirl-engine/state` (`OUTPUT=on|off`), more fan while not on the air, so the board
  is cool when the stream starts, and a quieter curve while streaming.
- The curve under a real stream, measured, and the profiles' curves from there.
