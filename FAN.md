> **Status:** IN PROGRESS (step 1, one curve, on the test board since Oct 15; profiles and the "camera" mode are next)
> **Updated:** 2026-10-15

# Fan control

The encoder drives the Jetson Nano's fan itself: `eirl-fan`, a small root daemon, sets the fan's speed from the
chip's temperature with a curve of ours. It replaces the kernel's own control, which leaves the fan stopped until
about 51 °C and then moves it in coarse steps, and the test board's old hand-installed `automagic-fan` (a Python
loop that ran the fan almost at full speed all the time). It is the first step of IMAGE.md's decision D-I7: thermal
profiles chosen in Settings and a mode like a professional camera's (much more fan than needed while not on the
air, quieter while streaming) come on top of it (§5).

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
  `error`, `at`). The journal gets a line when the fan starts, stops or reaches full speed, and one per hour with
  that hour's temperatures and speeds: `journalctl -u eirl-fan`.
- `eirl-fan -once`: one reading, printed, touching nothing.

Code: `manager/internal/fan` (the curve and the controller, with tests) and `manager/cmd/eirl-fan` (the daemon).
Unit: `manager/deploy/eirl-fan.service`: root, because the fan's speed is a root file in sysfs, but with no
capabilities, no network and a read-only system except its state directory.

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

Undo: `sudo systemctl disable --now eirl-fan` (the kernel's control comes back at once), then delete
`/usr/local/sbin/eirl-fan` and `/etc/systemd/system/eirl-fan.service`.

## 5. Next

- **Profiles**: the app writes the chosen one in `/etc/eirl-encoder` and the daemon reads it as data (a name from a
  fixed list, as `eirl-netsetup` reads its files: MANAGER.md §5); Settings and the web show `state.json`.
- **"Camera" mode**: from `/run/eirl-engine/state` (`OUTPUT=on|off`), more fan while not on the air, so the board
  is cool when the stream starts, and a quieter curve while streaming.
- The curve under a real stream, measured, and the profiles' curves from there.
