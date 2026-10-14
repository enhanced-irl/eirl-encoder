# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** by e-mail to **support@enhancedirl.com**, with
`[security] eirl-encoder` in the subject. Do not open a public issue or pull request for them.

Include what you can of:

- the affected component and version (the release version is shown in the device settings, and is in
  `/usr/lib/eirl-encoder/VERSION` on the board);
- how to reproduce it, and what an attacker needs (network position, credentials, physical access);
- the impact you expect.

We will acknowledge your report, keep you informed while we work on it and credit you in the release
notes if you wish. Please give us reasonable time to ship a fix before you disclose anything.

## Supported versions

Only the **latest release** receives security fixes. Devices update from the GitHub releases of this
repository, so a fix ships as a new release.

## Scope

In scope, the software in this repository as it runs on the device:

- the local web app and HTTP API (`eirl-encoderd`), its login and sessions, and the local socket used by
  the touch screen;
- the engine and its control socket;
- the privileged helpers and their privilege boundaries (sudoers and polkit rules, systemd units);
- the update mechanism: signature and checksum verification, downgrade protection, rollback;
- the device's own Wi-Fi access point and the reception of network cameras;
- the device side of the link with the EnhancedIRL cloud.

Out of scope:

- attacks that need physical access to the board (its console, its SD card or its pins): the Jetson Nano
  has no secure boot in this setup;
- vulnerabilities of third-party software (NVIDIA L4T, GStreamer, MediaMTX, srtla…), unless the way we
  use or configure it causes them: please report those upstream;
- the EnhancedIRL online services: you can report those to the same address, but they are not part of
  this repository.

Test only against devices and accounts you own, and never run denial-of-service tests against
EnhancedIRL's relays or services.
