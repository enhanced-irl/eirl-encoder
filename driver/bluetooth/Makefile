# SPDX-License-Identifier: GPL-2.0-only
# Bluetooth USB drivers with the Realtek RTL8761B(U), the chip of most of today's USB dongles (TP-Link UB500 and
# others), which L4T R32.7.6's 4.9 kernel lacks: btrtl learns its project id and firmware (rtl8761bu) and btusb
# its vendors' ids, both from upstream. Sources are the kernel's own, patched by build/build.sh (BLUETOOTH.md §3.8).
obj-m := btusb.o btrtl.o
