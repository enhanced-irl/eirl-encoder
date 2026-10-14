> **Status:** DEPLOYED on the test board and checked on screen (Oct 12). The app's `install.sh` installs it. Two minor tests remain (§6)
> **Updated:** 2026-10-14

# EnhancedIRL boot splash screen

With a screen on the encoder's HDMI you used to see the NVIDIA logo and then all of the system's
text. Now you see the EnhancedIRL logo with a progress bar while it boots, and when it finishes the
logo stays still and the screen is left free for the compositor (the mixer's touch UI).

## 1. What you see and why (measured on the board)

| Moment (since the kernel starts) | Before | Now |
|---|---|---|
| Firmware: CBoot and U-Boot, with the extlinux menu | NVIDIA logo (3 s menu) | NVIDIA logo (**1 s** menu) |
| 0 – 1.15 s | NVIDIA logo: the kernel inherits CBoot's image without cutting it (`tegra_fbmem`, "DC initialized, skipping tegra_dc_program_mode") | Same |
| 1.15 s | The text console attaches to `fb0` and the messages and the login appear | **It does not attach** (`fbcon=map:9`) |
| 1.32 – 1.40 s | Mode change 1920×1080 (CBoot's) → 1024×600 (the panel's EDID) | Same: the driver does it even without a console |
| 1.40 – 3.1 s | Text | **Black**: the driver zeroes `fb0` when changing mode (dump of `fb0` at 2.9 s, all zeros) |
| 2.75 s | systemd starts the units | `eirl-splash` starts |
| 3.1 s | — | **EnhancedIRL logo + bar** (0.34 s after the binary starts, competing for the CPU) |
| 8.5 s | `eirl-encoderd` active; `jetson login:` on screen | The bar is removed: **still logo**, screen free |

Full boot: 11.4 s, the same as before (`systemd-analyze`).

**The NVIDIA logo is not touched** (D1): CBoot draws it from the BMP partition of the QSPI and
changing it means flashing the QSPI, which the board rules forbid. On our own PCB (plan in
progress) the bootloader will be ours and the logo will show from the very first frame.

**Why not Plymouth** (installed, 0.9.3, but inactive): without `/dev/dri` it draws on `fb0`, on exit
it hands the screen back to the text console, and nothing of it can be reused in the mixer's UI or on the RK3588.

## 2. Decisions (Oct 12)

| | Decision |
|---|---|
| D1 | The firmware's NVIDIA logo stays (no flashing the QSPI) |
| D2 | When boot finishes, the still logo stays; then the compositor starts and draws over the screen |
| D3 | extlinux `TIMEOUT` from 3 s to 1 s (the menu is only visible on the serial console) |
| D4 | The text console on the HDMI only when boot fails |
| D5 | "Restarting…" and "Shutting down…" on screen when restarting and shutting down |

## 3. How it works

- **extlinux entry `eirl-splash`** (`eirl-splash boot-entry install`): a copy of the default entry
  (`tc358743`) with `fbcon=map:9` at the end of the APPEND, as `DEFAULT`, and `TIMEOUT 10`.
  `console=tty0 fbcon=map:0` come inside `${cbootargs}` (CBoot sets them) and cannot be removed.
  But `fb_console_setup` processes each `fbcon=` in order and the last one wins. With `map:9`, since there is no
  fb9, `fbcon_fb_registered` does not attach the console to any screen (verified in `fbcon.c` of
  4.9.337 and on the board). The messages still go to the serial console and the journal. It makes a backup
  (`extlinux.conf.bak-<date>-splash`), writes atomically and saves in
  `/var/lib/eirl-splash/extlinux.prev` the previous DEFAULT and TIMEOUT so it can be undone.
- **`eirl-splash`** (Go, `manager/cmd/eirl-splash` + `manager/internal/splash`): draws on `/dev/fb0`
  through `mmap`.
  - It reads the mode and the pixel format with `FBIOGET_*SCREENINFO` (on the board: 1024×600, 32 bpp,
    BGRA).
  - It draws at any resolution: the logo at ~15% of the height, the bar with antialiasing and the text
    in the Go Medium font, in the colours of the web's eirl theme.
  - Every 250 ms it checks whether the mode has changed (screen plugged in later, another EDID) and redraws.
    It redraws everything every 2 s in case something erases it.
  - On `SIGTERM` it leaves the still logo and exits without holding on to anything.
  - If nobody removes it within 3 min, it considers the boot failed and gives the console back.
  - If the text console has the screen (it was booted with another entry), it does not draw: it would write
    on top.
  - The logo is the landing page's `logo-dark.png` (742×155). The SVGs in that folder belong to the
    TailAdmin template, they are not the logo. At 1080p it is scaled up by 5%; if a vector logo shows up, switch to it.
- **Units** (`manager/deploy/splash/`):

| Unit | When | What it does |
|---|---|---|
| `eirl-splash.service` | `sysinit.target`, without default dependencies (~2.75 s) | Logo + bar until it is stopped. Hardened: no capabilities, only `/dev/fb0`, no network nor `/home` |
| `eirl-splash-quit.service` | `multi-user`, after `eirl-encoderd` | `systemctl stop eirl-splash` (blocking) |
| `eirl-splash-console.service` | `emergency.target` / `rescue.target` | `eirl-splash -console`: `FBIOPUT_CON2FBMAP` and the whole console comes back (D4) |
| `eirl-splash-reboot.service` / `-poweroff` | `reboot.target` / `poweroff.target` | Draws "Restarting…" / "Shutting down…" and exits; the image stays (D5) |

### Contract with the compositor (the touch-screen UI, [mixer](MIXER.md))

Nobody draws on the screen while `eirl-splash.service` is running. There are two ways to
comply, depending on what the process does:

**(a) A process that only draws.** Its unit has:

```ini
After=eirl-splash-quit.service          # starts with the screen already free
Before=eirl-splash-reboot.service eirl-splash-poweroff.service   # on shutdown, exits before the notice is drawn
```

The first one to comply was `eirl-mixer-demo.service`, the mixer's touch demo (Oct 12),
which was retired with F1 (MR !10).

**(b) A process that also captures and streams**, like `eirl-engine`, which since MR !10 is the
mixer's engine: it composes the program and draws the multiview through `nvdrmvideosink`. Streaming does **not**
wait for the splash, because delaying it ~8 s for the boot screen is not worth it, especially if the
board is coming back from a power cut during a live stream. So its unit does **not** have
`After=eirl-splash-quit.service`. It is the process itself that waits before attaching the screen
output, which runs in its own pipeline. Implementation: `SplashActive()` in
`manager/internal/engine/screen`; see [MIXER.md](MIXER.md), "Screen".

How it waits:

- Every ~250 ms it queries `systemctl show -p ActiveState --value eirl-splash.service`. It draws when
  that returns `inactive` or `failed`.
- `systemctl is-active` does not work: in L4T's systemd 237 it says "not active" also in
  `deactivating`, exactly when the splash draws the still logo upon receiving `SIGTERM`.
- Querying `eirl-splash-quit.service` does not work either. It is a oneshot without `RemainAfterExit` and is
  `inactive` both before and after running.
- The wait never hangs:
  - without the splash installed (another board, or after `--undo`) the unit does not exist and `show` already
    returns `inactive`;
  - if `eirl-encoderd` fails, `eirl-splash-quit` runs anyway, because `After=` only orders;
  - if nobody removes the splash, it removes itself after 3 min.
- The order at shutdown is already given by the splash units: `eirl-splash-reboot` and `-poweroff` have
  `After=… eirl-engine.service`. On shutdown, systemd stops that service before drawing the notice, whatever
  the direction of the order. That is why, **if the unit's name changes, it has to be changed
  there too**.

D2 is still met with both ways: the logo stays still until boot finishes, and
then the compositor appears.

Nothing else: the splash does not hold `fb0` when it exits and the text console does not write again. The screen's mode
is set by the driver when it detects the screen, not by the console (measured Oct 12: 1024×600 at 1.40 s
with no console attached). So `fb0/virtual_size` is still valid for drawing at the real
resolution.

## 4. Build, install and undo

It ships with the app: `JETSON=<board> manager/build.sh` builds it into `dist/pkg/splash/` and the app's
`install.sh` calls `splash/install.sh`. That installs the binary and the 5 units and creates or updates
the extlinux entry. It is idempotent: with everything up to date it does not touch `/boot`. It does not restart anything; the
changes show on the next boot.

Only the boot screen, or undoing it:

```bash
cd manager && ./build.sh
d=eirl-splash-$(date +%Y%m%d)
ssh <board> "mkdir -p ~/$d" && tar -C dist/pkg/splash -cf - . | ssh <board> "tar -C ~/$d -xf -"
ssh <board> "sudo ~/$d/install.sh"              # binary, 5 units, extlinux entry
ssh <board> "sudo ~/$d/install.sh --undo"   # removes the entry first, then the units and the binary
```

⚠️ After undoing it, the next deployment of the app installs it again. To remove it
completely, it has to be taken out of the package too.

Manual tests without rebooting:
- `eirl-splash -png f.png [-size 1920x1080] [-scene boot|ready|reboot|poweroff]` draws a scene
  without touching the screen.
- `sudo eirl-splash -scene boot -force` draws even with the console there.
- `sudo eirl-splash boot-entry install -dry` shows the resulting extlinux.

To see what is on screen:

```bash
sudo dd if=/dev/fb0 of=/tmp/fb.raw bs=4096 count=600
ffmpeg -f rawvideo -pix_fmt bgr0 -s 1024x600 -i fb.raw fb.png
```

Use `bgr0` and not `bgra`: the text console writes with alpha 0.

## 5. Tests done (Oct 12)

- `go test ./internal/splash`:
  - pixel format (BGRA, RGBA, XRGB, RGB565) and geometry validation;
  - composition at 6 resolutions, from 640×480 to 3840×2160;
  - scenes and bar;
  - console detection;
  - extlinux with the board's real file: the installer is idempotent and undoing gives back
    the original byte for byte, even if someone changed the DEFAULT afterwards.
- On the board, without rebooting:
  - reading the real mode;
  - drawing on the screen and reading `fb0` back: identical frame, colours right;
  - "Restarting…" and "Shutting down…" by starting their units by hand;
  - rescue: `eirl-splash-console` gives back the console with the login, and the splash then refuses
    to draw.
- **Real reboot (04:26)**:
  - `fbcon=map:9` wins: only the empty (dummy) console attached and no "switching to colour
    frame buffer";
  - the splash draws at 3.09 s and is removed at 8.51 s;
  - boot in 11.4 s and no failed units;
  - streaming and `eirl-encoderd` start as always.
- **Second reboot (04:33)**, with a temporary unit that dumped `fb0` right before the splash:
  - at 2.9 s the screen memory is entirely zero, so the gap between the NVIDIA logo
    and the EnhancedIRL one is black, not leftovers or garbage;
  - the splash draws at 3.27 s and is removed at 8.35 s;
  - boot in 11.1 s.
- **With `eirl-engine` as the mixer's engine (13:22, variant (b) of the contract)**, measured by the
  mixer workstream with the Cam Link hung (USB kept retrying to read its descriptor, `-110`):
  - **Before**: boot in 42.4 s. `eirl-hdmi-init.service` required `systemd-udev-settle`, which runs
    `Before=sysinit.target` and holds back the whole boot. With the Cam Link hung it waited 34.3 s.
    The splash held up: it runs outside `sysinit` and showed the logo until 39.8 s.
  - **After removing that dependency** (the script already waits for the TC358743 on its own):
    - boot in 9.9 s;
    - `eirl-engine` starts at 7.17 s, without waiting for the splash;
    - the splash is removed at 7.42 s;
    - the multiview appears at 14.4 s. That included 3 s of waiting for the screen to be stable,
      which are no longer needed when arriving after the splash. Without them it is ~11 s.
  - The Logitech and the touch panel still show up at ~37 s, because they hang off the same port as the
    Cam Link. The engine picks them up when they appear and they no longer delay streaming or the screen.

## 6. Pending

Checked on screen on Oct 12: "everything works great".
If the ~1.7 s black gap (from 1.4 to 3.1 s) ever becomes a nuisance, it can be shortened by drawing from
a copy of the initrd (`INITRD /boot/initrd-eirl`), which gets to ~1.5 s.

1. Plug in the screen with the splash running (`sudo eirl-splash -scene boot -timeout 10m`) and check
   that it adapts to the new mode.
2. A real failed boot (`systemd.unit=emergency.target` in a test entry). Careful: with the
   root account locked, `sulogin` waits for an Enter on the console, so a keyboard is needed.
3. Product image: the extlinux entry and the units should come built in, without having to run `install.sh`.
