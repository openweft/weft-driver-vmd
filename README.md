# weft-driver-vmd

OpenBSD `vmd(8)` hypervisor driver for openweft — the third sibling in the
`weft-driver-*` family after [`weft-driver-vz`](https://github.com/openweft/weft-driver-vz)
(Apple Virtualization framework on macOS) and
[`weft-driver-qemu`](https://github.com/openweft/weft-driver-qemu)
(QEMU/KVM on Linux).

**Status: scaffolding.** The four driver services (`Hypervisor`, `Network`,
`Volume`, `Image`) compile and serve over [go-plugin](https://github.com/hashicorp/go-plugin),
the lifecycle methods shell out to `vmctl(8)` with the right arguments, and
features that don't fit vmd's day-1 model (NIC hot-plug, dynamic networks,
cluster volumes, image caching) return `drivers.ErrUnsupported`. The pure-Go
argument builder in `builtin/args.go` is unit-tested so the wiring is
correct without an OpenBSD host in the loop.

## Why a separate driver

OpenBSD's `vmd` is the platform's first-party hypervisor: pure Linux /
OpenBSD direct-kernel boot, no UEFI, no firmware blobs, integrated with
`pf(4)` for the data path. It's the cleanest place on the BSDs to run a
small fleet of Linux micro-VMs, and the openweft landing page cites it
explicitly as "the next sibling" after QEMU.

The driver is pure-Go, shells out to the host's `vmctl(8)` binary, and
needs no cgo — same recipe `weft-driver-qemu` uses for QEMU. That means
the binary cross-builds from a macOS dev host and runs on
`openbsd/amd64` and `openbsd/arm64` in production.

## Architecture

```
+------------------+         +--------------------+         +-----------+
|   weft-agent     | plugin  |  weft-driver-vmd   | vmctl   |  vmd(8)   |
|  (control plane) | ------> |  (this -- one per  | ------> |  (root)   |
|                  |  gRPC   |   OpenBSD host)    |  exec   |           |
+------------------+         +--------------------+         +-----------+
```

- `weft-agent` launches `weft-driver-vmd` as a go-plugin process and
  dispenses a `DriverSet` over the gRPC transport.
- The driver translates `drivers.VMSpec` etc. into `vmctl start/stop/status`
  argument lists (see `builtin/args.go`) and execs `vmctl` (or `doas vmctl`
  if the binary is configured that way).
- `vmd(8)` does the actual virtualisation, integrating with `pf(4)` for the
  tap/bridge layer.

## What vmd does (and doesn't) support

`vmd` only does direct-kernel boot of Linux + OpenBSD guests -- no UEFI,
no EFI ISO boot, no Windows. The `HypervisorDriver` surface area maps as:

| Method               | Behaviour                                                                |
| -------------------- | ------------------------------------------------------------------------ |
| `CreateVM`           | Allocates the VM directory and a stable MAC (idempotent).                |
| `StartVM`            | `vmctl start <name> -k <kernel> -d <disk> -m <mem> -L`                   |
| `StopVM`             | `vmctl stop <name>` (idempotent -- missing VM is no-op).                 |
| `DeleteVM`           | `vmctl stop -f` if running, then removes the VM state directory.         |
| `AttachDisk`         | `vmctl create -s <size> <path>` when the backing file is missing.        |
| `DetachDisk`         | No-op (vmd binds disks at boot, no hot-plug -- idempotent return).       |
| `AttachNIC`          | `drivers.ErrUnsupported` -- NICs are wired at `vmctl start` time.        |
| `DetachNIC`          | `drivers.ErrUnsupported`.                                                |
| `Network*` services  | `drivers.ErrUnsupported` until the `pf(4)` driver lands.                 |
| `Volume*` services   | `drivers.ErrUnsupported` -- `AttachDisk` covers the transitional path.   |
| `Image*` services    | `drivers.ErrUnsupported` (in-cache returns `false`).                     |

## Build

```sh
# Host build (dev -- darwin/arm64 from your macOS workstation)
pkgx task build

# Cross-build the OpenBSD binaries that actually run on the hypervisor host
pkgx task build-openbsd
```

The cross-build produces `dist/weft-driver-vmd-openbsd-amd64` and
`dist/weft-driver-vmd-openbsd-arm64`.

## Run

`weft-driver-vmd` is launched by `weft-agent` over go-plugin -- never
directly by an operator. For local debugging:

```sh
WEFT_DRIVER_HOST_UUID=test                              \
WEFT_DRIVER_HOSTNAME=$(hostname)                        \
WEFT_DRIVER_STATE_DIR=/var/db/weft                      \
WEFT_VMCTL_BINARY=/usr/sbin/vmctl                       \
./weft-driver-vmd
```

(That handshake will hang -- go-plugin expects to be spawned by a host,
not run interactively. Use it to confirm the binary at least starts.)

## License

BSD 3-Clause -- see [LICENSE](LICENSE). Same license as the rest of the
greenfield openweft code base.
