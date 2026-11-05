> **Status:** IN PROGRESS — wired and tested on the test board on 2026-11-05; the second fan's thermal control is next
> **Updated:** 2026-11-05

# Building the hardware yourself: power, HDMI reset and a second fan

A guide for whoever builds an encoder on a Jetson Nano B01 with parts they can buy, and no custom PCB. It adds
three things to the board described in [`DEVELOPMENT.md`](DEVELOPMENT.md) ("Hardware"):

- **Power from 12–15 V** (a DC supply or a USB-C PD charger) with two 5 V buck modules: one for the Jetson,
  one for the USB hub, so the USB load never pulls the Jetson's 5 V down.
- **A hardware reset line for the TC358743.** It is the only thing that brings the chip back when its audio PLL
  stays unlocked ([`DEVELOPMENT.md`](DEVELOPMENT.md), "Audio that does not come back"), and the engine can only
  request it automatically when the reset pin is in the device tree.
- **A second 5 V fan** for the case, switched on and off by the Jetson.

Everything here was wired and measured on the test board. Where a value is the vendor's or a datasheet's, it
says so.

## 1. The rule that shapes everything: the 40-pin header is behind level shifters

On the B01 the 40-pin header goes through **TXB0108** auto-direction level shifters. Seen from the outside, every
pin is a weak output, about 4 kΩ.

- **It cannot drive a load:** no LEDs, no optocouplers, no relay modules, no MOSFET gates directly. Whatever a
  header pin drives must be a high-impedance input: CMOS, with a pull-down of 50 kΩ or more.
- **It fights capacitors and pull-ups.** The TC358743 board's RST pad has a pull-up and a capacitor. A direct
  wire from a header pin to it left the chip held in reset, even after a power cycle (DEVELOPMENT.md,
  "Hardware reset").
- **A pin configured as an input keeps the last level forced on it.** At boot, pins 32 and 33 have come up high
  on some boots and low on others. Touching pin 18 with a meter probe that also touched pin 17 (3.3 V) left it
  high. **Every pin that controls something must be driven as an output from boot.**

That is why a **DRV8833** dual H-bridge module sits between the Jetson and both the reset line and the fan. Its
inputs are CMOS with internal pull-downs (150 kΩ typical, TI datasheet SLVSAR1E), and its outputs do the
driving.

## 2. Parts

| Part | Notes |
|---|---|
| 2 × **MINI560** 5 V / 5 A buck module | Input 7–20 V. The chip's marking is erased; it is a JoulWatt JW5069A or JW5068A. **Buy the 5 V version**: it is also sold for 3.3, 9 and 12 V |
| 1 × **DRV8833** dual H-bridge module | Pins `IN1`–`IN4`, `OUT1`–`OUT4`, `VCC`, `GND`, `EEP` (nSLEEP), `ULT` (nFAULT). Some modules call them `AIN1/AIN2/BIN1/BIN2` and `AOUT1/AOUT2/BOUT1/BOUT2` |
| 5 V fan, 2 wires (30 mm on the test board) | Only switched on and off, see §6 |
| 12–15 V source | A DC supply, or a USB-C PD trigger board set to **12 or 15 V** (not 20 V: it is the MINI560's limit) |
| 15-pin, 1 mm pitch camera flat cable | The Raspberry Pi 2/3/4 camera type, **not** the 22-pin 0.5 mm one. Keep it short; one with the contacts on the **same** side at both ends fits the B01 and the HDMI board |
| Wire, heat-shrink, Kapton tape | 18 AWG for the 5 V runs to the Jetson and the hub; the 100 kΩ resistor comes from the MINI560 itself |

A multimeter you trust. The one used for this build read about **20 % high** (6.0 V on the header's 5 V pin, 3.9 V
on its 3.3 V pin) until it was checked against the Jetson's own power monitor. **Check yours on pin 1 (3.3 V) first.**

## 3. Power: two MINI560

```
12–15 V (+) ──┬──► IN+  MINI560 #1 ──► OUT+ ──► Jetson 5 V input
              │         EN: as it comes from the factory
              │
              └──► IN+  MINI560 #2 ──► OUT+ ──► USB hub (its own power input)
                        EN: factory resistor to IN+ removed; EN ──► header pin 1 (3.3 V)   └─► DRV8833 VCC

12–15 V (−) ── GND of both modules ── Jetson GND ── hub GND   (in the MINI560, IN− and OUT− are the same node)
```

**The EN pin.** On the modules used here, EN has a factory divider: 100 kΩ (marked `104`) from EN to IN+ and
another 100 kΩ from EN to IN− (GND). EN sits at half the input, so the module is on whenever it has power.
Another revision of the board is described online with no resistor on EN, so look at yours. According to the
JW5068A's (preliminary) datasheet:
- EN turns the chip on above 0.8 V and off below 0.4 V;
- its absolute maximum is **6 V**: **never tie EN straight to the 12–15 V input**;
- switched off it draws 2–5 µA.

- **MINI560 #1 (Jetson):** leave it as it is.
- **MINI560 #2 (hub):** desolder the 100 kΩ that goes to IN+. To find it, check continuity from each resistor's
  outer pad to the IN+ terminal. The one to GND stays, and it now holds EN low: the module is **off** until EN is
  driven. Then wire EN to the header's **3.3 V (pin 1 or 17)**. The hub and the DRV8833 then come on with the
  Jetson, also during boot.

  The 3.3 V rail can take this: two 100 kΩ inputs draw tens of µA. The capacitor next to the removed resistor
  belonged to it: check it has no continuity with EN.

  **Optional: the Jetson switches the hub.** EN can go to a header GPIO instead (tested with pin 18): the hub
  stays off until software drives the pin high, and the Jetson can power-cycle it. With the factory resistor
  removed, EN takes a header pin directly (about 3.2 V when high). **Never wire a header pin to an EN that still
  has its resistor to IN+**: it would push 5–6 V into a 3.3 V pin, turn the hub on while the Jetson is off, and the
  TXB0108 could not pull it below the 0.4 V the chip needs to switch off. The reference build keeps EN at 3.3 V
  because it is simpler.

**Before you connect the Jetson:**
1. Measure both outputs: 5.0–5.2 V (against a trusted meter).
2. For MINI560 #2, with its resistor removed:
   - with EN left free, the output must be 0 V;
   - with EN at 3.3 V, it must give 5 V;
   - unpowered, EN to IN+ must read megohms (no solder bridge left) and EN to GND about 100 kΩ.

**Measured on the test board** (the Jetson's `ina3221`, `POM_5V_IN`): 4.98–5.04 V at 0.6–1.05 A. Each module
draws about 2.5 mA at idle. MINI560 #2 near 4 A runs close to its 5 A rating on a very small board: give it some
airflow.

If the hub also takes power from the Jetson's USB port while its own supply is off, put Kapton tape over the
VBUS contact of the hub's upstream plug.

## 4. The DRV8833

| DRV8833 pin | Goes to | Why |
|---|---|---|
| `VCC` (VM) | MINI560 #2 output, or the header's 5 V (pin 2 or 4) | Either works, **as long as it is powered whenever the Jetson is**. See the trap below |
| `GND` | Common GND (header pin 6) | |
| `EEP` (nSLEEP) | Header **pin 1** (3.3 V) | Awake whenever the Jetson's 3.3 V is up. nSLEEP needs ≥ 2.5 V and has a 500 kΩ pull-down, so it goes to the fixed rail, not to a GPIO |
| `ULT` (nFAULT) | Nothing | Fault output, not used |
| `IN1` | Header **pin 32** (GPIO, `gpio168`, PV0) | Fan on/off |
| `IN2` | GND of the module | (Its internal pull-down would do too) |
| `IN3` | GND of the module | **Recommended**: if IN3 went high while IN4 is low, OUT3 would put VCC on the RST line |
| `IN4` | Header **pin 33** (GPIO, `gpio38`, PE6) | Reset |
| `OUT1` / `OUT2` | Fan red / fan black | |
| `OUT3` | **RST** pad of the TC358743 board | |
| `OUT4` | **Nothing** | It carries VCC while the reset is held: never connect it to RST |

Truth table of each bridge: `00` = both outputs open, `10` = OUT1 (OUT3) high, `01` = OUT2 (OUT4) high, `11` =
both low.

- **Fan:** IN1 = 1, IN2 = 0 → OUT1 high, OUT2 low → it spins. IN1 = 0 → open → it stops.
- **Reset:** IN3 = 0, IN4 = 1 → OUT3 **low**: the chip is held in reset. IN4 = 0 → OUT3 **open**: the HDMI board's
  pull-up releases the reset. OUT3 only ever pulls to GND or lets go, so it never puts VCC on the chip. The
  TXB0108 only sees a CMOS input.

**Traps**
- **An unpowered DRV8833 holds the HDMI chip in reset.** Each output has a diode to VCC. If VCC is down (for example
  fed from a regulator that is off), OUT3 clamps the RST line to about 0.7 V. That is why VCC must be up whenever
  the Jetson is.
- **A meter on an open output lies.** With IN4 = 0, OUT3 measured on its own read about VCC + 0.6 V, through
  leakage and the diode. It is not a real source: with a 100 kΩ load, or once it is connected to the RST pad, it
  goes away.
- **Wire with the Jetson unplugged.** On the test board `poweroff` boots it again after a few seconds.

**Checks without software**, with the Jetson running:
1. **Fan:** move IN1 to pin 1 for a moment: the fan must spin at full speed. Put it back on pin 32.
2. **Reset**, with the RST wire **not yet** soldered to the HDMI board: move IN4 to pin 1 for a moment, or drive
   pin 33 high (§5). OUT4 must read VCC and OUT3 0 V. Then OUT3 must never read VCC in any state.

## 5. The TC358743 reset

**Device tree.** In the TC358743 node:

```dts
reset-gpios = <&gpio TEGRA_GPIO(E, 6) GPIO_ACTIVE_HIGH>;   /* pin 33 high = OUT3 low = RESETN low */
```

The dual DTB (`…-b00-tc358743-dual.dtb`, DEVELOPMENT.md, "HDMI over CSI on CAM0 and CAM1") declares it, on both
connectors' nodes: **PE6 (pin 33) and `GPIO_ACTIVE_HIGH`**, the polarity of the open-drain transistor DEVELOPMENT.md
describes. The frozen base's DTB (`tegra210-porg-camera-tc358743.dtsi`) keeps the line commented out, with pin 31
and `GPIO_ACTIVE_LOW`, from the direct-wire experiment of October.

**Do not wire the reset to a board that boots the base's DTB.** Nobody owns pin 33 then, and after a restart it may
rest high (the TXB0108 keeps the last level, or the pull-up wins): the DRV8833 then holds the chip in reset and there
is no HDMI. Seen on Nov 5 with a DTB without the pin: PE6 an input reading 1, the chip silent on I2C. The dual DTB
comes with the updates (UPDATES.md §12.11) and the splash's boot entry boots it.

With the pin declared:
- The driver takes the line as an output at probe, releases the reset, and waits until the chip answers.
- The chip gets the `hardware_reset` control (a chip without the pin has none).
- The engine's last audio recovery step (a hardware reset at the next start, with the pipeline stopped) becomes
  available.

**Tested on 2026-11-05.** The test board runs the dual DTB, with the HDMI board's flat cable on CAM0 (see the note at
the end).

| Test | Result |
|---|---|
| Manual pulse of 10 ms on pin 33, engine stopped | During the pulse the chip does not answer over I2C. After it, its registers are back at their power-on values (`SYSCTL` 0x0C00 → 0x0001, `CONFCTL` 0x0CD4 → 0x0000, `FIFOCTL` 0x014B → 0x0010): a real reset |
| Reset at probe, with the pin in the DT | Chip ready **108 ms** after the reset, at the first attempt; `gpio-38` held by the driver ("reset", output low) |
| `v4l2-ctl -d /dev/v4l-subdev1 -c hardware_reset=1`, engine stopped | Pulse and full reprogramming (EDID included). `SYS_STATUS` 0x9f: the HDMI signal is locked again. Then HDMI live at 1080p30 in the engine |

**Manual pulse, before declaring the pin** (only if no driver holds the line):

```bash
sudo sh -c 'echo 38 > /sys/class/gpio/export; echo low > /sys/class/gpio/gpio38/direction'
sudo sh -c 'echo 1 > /sys/class/gpio/gpio38/value; sleep 0.01; echo 0 > /sys/class/gpio/gpio38/value'
```

After a manual pulse the chip is unconfigured until the driver reprograms it. That needs the pin in the DT
(`hardware_reset`) or a reboot.

**Reading the RST pad.** With the HDMI board powered (its LED on) and the RST wire loose, the pad reads about
3.0–3.3 V: the board's pull-up. **0 V means the HDMI board has no power**: check the flat cable. On the test board,
a cracked cable made the chip stop answering for a day before it was found.

## 6. The second fan

**On and off only, at full speed.** Speed control by PWM was tried and rejected. The pin's pad was switched at
run time to the Jetson's `pwm0`, the same controller as the main fan (which uses `pwm-3`), and the results were:

| PWM | Result |
|---|---|
| 22 kHz (inaudible) | The fan stalls below 100 %: tried at 50, 70 and 90 % |
| 30, 100 and 200 Hz at 50 % | It spins slowly, but loud |

A 2-wire fan's own electronics do not survive the off-time of each cycle. For speed control you would need a
filter at the fan: about 4.7 Ω in series and 470 µF across the fan, so it sees a smooth DC voltage. **Not tested.**

**Driving it.** Pin 32 as a plain GPIO output: 1 = on, 0 = off.

```bash
sudo sh -c 'echo 168 > /sys/class/gpio/export; echo high > /sys/class/gpio/gpio168/direction'   # on
sudo sh -c 'echo 0 > /sys/class/gpio/gpio168/value'                                               # off
```

Because of §1, until some software drives pin 32 it may come up on or off. Listing the GPIO in the device tree's
`gpio-output-low` (the `gpio-init` node) did **not** take effect on `gpio168` in the test, so whoever controls the
fan must take the pin as an output when it starts. The automatic control (on above a CPU temperature, off below
a lower one) goes in the fan daemon: [`FAN.md`](FAN.md).

## 7. Header pins used

| Pin | Use |
|---|---|
| 1 | 3.3 V: the DRV8833's `EEP`, and the EN of MINI560 #2 (or pin 17) |
| 2 / 4 | 5 V (only if the DRV8833's VCC is taken from here) |
| 6 | GND for the DRV8833 |
| 12, 35, 38, 39 | HDMI audio over I2S4 (DEVELOPMENT.md, "Hardware"). **Do not connect pin 40** |
| 32 | Second fan (DRV8833 IN1) |
| 33 | TC358743 reset (DRV8833 IN4) |
| 18 | Free in the reference build (tested as the hub's EN) |

Avoid pins 24 and 26 for anything that must start off: they rest high. Pin 31 rests high as well.

**A note on the camera connector.** The image's DTB (the frozen base's) has the TC358743 on **CAM1** (I2C bus 8)
only: with it, use CAM1. The dual DTB finds it on **CAM0** or **CAM1**, or one on each (two HDMI inputs, the second
without audio), and puts the reset pin on whichever has it; it comes with the updates (UPDATES.md §12.11; DEVELOPMENT.md,
"HDMI over CSI on CAM0 and CAM1"). With two HDMI boards, `HDMI_WIRED=cam0|cam1` says which one has the audio and reset
wires.
