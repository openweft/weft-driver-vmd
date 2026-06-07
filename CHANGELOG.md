# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Operational driver via direct imsg(3) transport.** The driver
  speaks vmd's binary control protocol on `/var/run/vmd.sock`
  directly — no `vmctl(8)` exec, no text parsing, no per-call
  fork+exec overhead. The imsg framing + vmd message types
  (`IMSG_VMDOP_START_VM_REQUEST` / `_RESPONSE` /
  `IMSG_VMDOP_TERMINATE_VM_REQUEST` / `_RESPONSE` /
  `IMSG_VMDOP_GET_INFO_VM_REQUEST` / `_RESPONSE` /
  `_END_DATA`) are implemented in pure Go in
  `builtin/imsg.go` + `builtin/imsg_vmd.go`. Zero new deps.
- **HypervisorDriver fully wired** :
  `CreateVM` (persists per-VM spec under `<StateDir>/<uuid>/spec.json`),
  `StartVM` (rebuilds the start payload from spec + sends imsg),
  `StopVM` (graceful terminate, escalates to force on ctx deadline),
  `DeleteVM` (force-terminate + state-dir cleanup),
  `AttachDisk` / `DetachDisk` / `AttachNIC` / `DetachNIC`
  (persisted into spec ; vmd has no hotplug — bindings take
  effect on next `StartVM`).
- **VolumeDriver wired** :
  `EnsureVolume` provisions a raw disk image at
  `<StateDir>/disks/<uuid>.img` via plain `os.Truncate` (vmd treats
  truncated files as flat block devices, no qcow2 magic header
  needed). Grow-only sizing enforced ; idempotent.
  `DestroyVolume` is `os.Remove` (collapses ErrNotExist to nil).
  `AttachVolume` returns the local path.
- **11 unit tests** for the imsg encoding / decoding paths : struct
  layout vs `vmd.h` offsets, response parsing (including error_string
  surfacing), VM state enum mapping, name truncation + NUL
  termination, padded-string helper. No vmd socket required — the
  on-wire shape is exercised directly.
- Cross-builds on darwin/arm64 (vet), openbsd/amd64 (production
  target), openbsd/arm64 (Apple Silicon / Ampere).

### Removed

- **`vmctl(8)` exec path.** The README's original "v0.1 shells out
  to `vmctl`, v0.2 switches to imsg" roadmap was collapsed into a
  single imsg-direct release. No production driver should pay the
  ~30 ms per-call fork+exec tax when the binary protocol is
  trivially implementable in pure Go.

## [Earlier scaffold release] — 2026-06 (superseded above)

### Added

- Initial scaffold for the OpenBSD vmd(8) hypervisor driver. Third
  backend alongside `weft-driver-vz` (macOS Apple Virtualization) and
  `weft-driver-qemu` (Linux QEMU/KVM). vmd is OpenBSD's native
  pledge/unveil-hardened hypervisor — small attack surface, zero
  third-party kernel modules.
- Plugin handshake + go-plugin gRPC server in place ; the four driver
  interfaces (Hypervisor, Volume, Network, Image) are wired and return
  `drivers.ErrUnsupported` from every method until the vmctl(8)
  integration ships. The driver registers cleanly through go-plugin
  so an operator can wire it into `cluster.hcl` and verify discovery.
- `builtin/bundle.go` : `Options` (HostUUID, Hostname, VmctlBinary,
  VmdSocket, StateDir, SwitchName, Logger) + `NewBundle` constructor.
  Defaults match an out-of-the-box OpenBSD install (`vmctl` on PATH,
  `/var/run/vmd.sock`, `/var/lib/weft/vmd`).
- `builtin/vmd.go` : `vmdHypervisor` skeleton driven by `vmctl(8)`
  exec. The v0.1 path shells out to `vmctl start / stop / status` ;
  the imsg socket integration (event-driven, structured) is a future
  milestone.
- `builtin/volume.go` : `vmdVolume` for raw / qcow2 image files under
  StateDir. `Local()` returns true ; cluster-managed block storage
  routes through weft-block.
- `builtin/network.go` : `vmdNetwork` for vether(4) virtual switches +
  tap(4) per-VM interfaces. `RotateMeshPeer` returns
  `ErrNotApplicable` (WireGuard mesh peers run as overlays managed by
  weft-network, not by the host hypervisor driver).
- `builtin/image.go` : `vmdImage` for OCI → raw disk conversion.
- `cmd/weft-driver-vmd/main.go` : plugin entrypoint, reads
  `WEFT_VMD_VMCTL_BINARY`, `WEFT_VMD_SOCKET`, `WEFT_VMD_SWITCH` from
  env (with sensible OpenBSD defaults).
- `README.md` documents the rationale and the seven implementation
  follow-ons (vmctl start → status polling → stop → disks → networking
  → images → imsg socket).

### Important : transport

The driver speaks **only** the standard openweft gRPC plugin
contract (go-plugin handshake over stdin/stdout). There is no REST
or HTTP surface — vmd(8) is local to the host and exposed through
the OpenBSD-native `vmctl(8)` CLI and the imsg socket at
`/var/run/vmd.sock`.

### Status

This is a **scaffold release**. Functional VM/volume/network/image
operations are the next milestones (rough order in the README).
