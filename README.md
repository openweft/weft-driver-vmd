<p align="center"><img src="https://raw.githubusercontent.com/openweft/brand/main/social/openweft.png" alt="openweft" width="720"></p>

# weft-driver-vmd

**OpenBSD vmd hypervisor driver for weft** — the third backend
alongside `weft-driver-vz` (Apple Virtualization on macOS) and
`weft-driver-qemu` (QEMU/KVM on Linux). One driver per host OS, one
hyperviseur native each :

| Backend | OS      | Hypervisor                  |
| ------- | ------- | --------------------------- |
| vz      | macOS   | Apple Virtualization Framework |
| qemu    | Linux   | QEMU/KVM                    |
| **vmd** | OpenBSD | vmd(8) / vmctl(8) / vm.conf |

## What it is

[OpenBSD `vmd`](https://man.openbsd.org/vmd.8) is the OpenBSD kernel's
native hypervisor : a small, pledge/unveil-hardened daemon that runs
VMs through the `vmm(4)` syscall surface. Workloads boot as standard
OpenBSD or Linux guests ; storage is qcow2/raw image files ; networking
is tap interfaces on a bridge.

This driver exposes the standard weft `HypervisorDriver` /
`VolumeDriver` / `NetworkDriver` / `ImageDriver` interfaces through
the openweft go-plugin gRPC contract. It speaks directly to vmd's
**imsg(3) control socket** at `/var/run/vmd.sock` — no `vmctl(8)`
exec, no text parsing. The imsg framing, the vmd-specific message
types, and the file-descriptor passing are all hand-rolled in
[`builtin/imsg.go`](builtin/imsg.go) +
[`builtin/imsg_vmd.go`](builtin/imsg_vmd.go) — pure Go, zero new
deps, builds cross-platform.

Why direct imsg vs `vmctl(8)` exec :

- **No per-call process spawn** : `vmctl` fork+exec is ~30 ms ;
  the imsg request/response round-trip on the Unix socket is
  sub-millisecond.
- **Binary framing, no parsing surprise** : every field comes off
  the wire in a typed slice — no whitespace splitting, no version-
  drift fragility across OpenBSD releases.
- **Synchronous status** : vmd's `START_VM_RESPONSE` carries the
  assigned VM ID and the success/error codes inline, so we don't
  need to poll after `start`.

## Status — operational (2026-06)

The driver speaks vmd's imsg protocol end-to-end :

- `CreateVM` / `StartVM` / `StopVM` / `DeleteVM` / `AttachDisk` /
  `DetachDisk` / `AttachNIC` / `DetachNIC` all implemented.
- `EnsureVolume` / `DestroyVolume` / `AttachVolume` provision raw
  disk images under `<StateDir>/disks/<uuid>.img` ; grow-only sizing
  enforced ; idempotent.
- 11 unit tests cover the imsg encode / decode paths (struct layout
  vs `vmd.h`, response parsing including error_string surfacing,
  vm-state enum mapping, name truncation + NUL termination).

imsg message types covered (sourced from `usr.sbin/vmd/vmd.h`) :

- `IMSG_VMDOP_START_VM_REQUEST` / `_RESPONSE` — CreateVM + StartVM
- `IMSG_VMDOP_TERMINATE_VM_REQUEST` / `_RESPONSE` — StopVM + DeleteVM
- `IMSG_VMDOP_GET_INFO_VM_REQUEST` / `_RESPONSE` / `_END_DATA` —
  VM status lookup

NetworkDriver and ImageDriver stay as `ErrUnsupported` by design :
- Networks are owned by `weft-network` (mesh / vether / tap fan-out
  lives there ; the driver isn't the source of truth).
- Images are pulled host-side by `weft microvm pull` and materialised
  into the StateDir before the driver sees them.

What's still pending :

- Console PTY proxy (vmd attaches a tty via SCM_RIGHTS ; the driver
  has the imsg ancillary-data hook but the go-plugin side hasn't
  wired the operator-facing `weft microvm console` yet).
- Snapshot / backup ops on VolumeDriver — they route through
  `weft-block` (`Name="block"`) for cluster-replicated semantics.

## What the driver is for

- Operators running OpenBSD as their hypervisor host (small fleets,
  pledge/unveil-hardened control planes, defence-in-depth deployments).
- Federated clusters where OpenBSD hosts sit alongside macOS dev boxes
  and Linux production hosts. Same openweft surface, same catalogue,
  same `weft microvm pull` workflow ; only the substrate changes.

The driver does NOT virtualise via QEMU running on OpenBSD — that's a
different binary, a different config, and a different security model.
This driver is **strictly the OpenBSD-native vmd(8)** ; QEMU on OpenBSD
operators should use `weft-driver-qemu` instead.

## Layout

| Path                              | Purpose                                                |
| --------------------------------- | ------------------------------------------------------ |
| `cmd/weft-driver-vmd/main.go`     | Plugin entrypoint ; serves the gRPC contract           |
| `builtin/bundle.go`               | Bundle assembling the four driver interfaces           |
| `builtin/imsg.go`                 | Pure-Go imsg(3) client : framing + Unix-socket I/O     |
| `builtin/imsg_vmd.go`             | vmd-specific imsg messages (START / TERMINATE / INFO)  |
| `builtin/imsg_test.go`            | Unit tests for the encode / decode paths               |
| `builtin/vmd.go`                  | HypervisorDriver impl over the imsg client             |
| `builtin/volume.go`               | VolumeDriver impl (raw image files under StateDir)     |
| `builtin/network.go`              | NetworkDriver impl (delegated to weft-network)         |
| `builtin/image.go`                | ImageDriver impl (delegated to host imagestore)        |

## Build + register

```sh
cd weft-driver-vmd
GOOS=openbsd GOARCH=amd64 go build -o weft-driver-vmd ./cmd/weft-driver-vmd

# Register in cluster.hcl on the OpenBSD host :
#   drivers {
#     vmd = "ghcr.io/openweft/weft-driver-vmd:v0.1.0"
#   }
```

The plugin contract is **gRPC over the standard go-plugin handshake**
(stdin/stdout multiplexed) — no extra ports, no extra config. weft-agent
exec's the binary, performs the handshake, and dispatches HypervisorDriver
calls over the in-process gRPC channel.

## Next steps (rough order)

1. Wire `vmctl start` via `os/exec` against an in-memory `vm.conf`
   fragment. Parse exit codes ; success = VM running.
2. `vmctl status -r` polling for VMStatus reads (until imsg lands).
3. `vmctl stop -f` for hard StopVM ; graceful via `vmctl stop`.
4. Disk lifecycle : `vmctl create <path> -s <gib>` for EnsureVolume,
   `unlink()` for DestroyVolume.
5. Network : `ifconfig vether<N> create` + `vmctl start -n <switch>`.
6. Image driver : OCI pull → tarball → raw image conversion.
7. Switch to **imsg socket** when the `vmctl` path is stable enough
   to need event-driven state.
