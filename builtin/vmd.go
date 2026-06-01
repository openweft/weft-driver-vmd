// Package builtin implements an OpenBSD vmd(8) HypervisorDriver. The driver
// is pure Go : it never links the hypervisor in-process, it execs vmctl(8)
// with arguments composed by args.go. That keeps the binary CGO_ENABLED=0
// and trivially cross-buildable from a macOS or Linux dev host.
//
// vmd is the BSDs' first-party hypervisor : only direct-kernel boot of
// Linux + OpenBSD guests, no UEFI, no firmware blobs. The HypervisorDriver
// surface area maps cleanly :
//
//   CreateVM / StartVM / StopVM / DeleteVM      vmctl start / stop
//   AttachDisk                                  vmctl create
//   DetachDisk                                  no-op (vmd binds disks at boot)
//   AttachNIC / DetachNIC                       drivers.ErrUnsupported
//                                               (NICs are wired at start time)
//
// VM lifetime follows the same transitional convention as weft-driver-vz
// and weft-driver-qemu : vmUUID is the absolute path to the VM's state
// directory. By convention that directory holds : kernel, disk.img
// (optional), mac.txt, name (the vmctl VM name -- derived from
// filepath.Base(vmDir) at CreateVM time).
package builtin

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	drivers "github.com/openweft/weft-drivers"
)

// Options configures a vmd driver instance.
type Options struct {
	// VmctlBinary is the vmctl(8) executable. Empty -> "vmctl" resolved on
	// PATH. Set to e.g. "doas" + first-arg "vmctl" when the driver runs
	// non-root and the host's doas.conf grants vmctl access ; the driver
	// passes VmctlBinary verbatim to exec.Command so the value can be a
	// shell command with embedded args provided it's tokenised by the
	// caller (we don't shell-split here -- pass an absolute path or a
	// wrapper script).
	VmctlBinary string
	// DefaultMemMiB is used when a VMSpec leaves MemoryMiB unset.
	DefaultMemMiB int
	// DefaultCPUs is used when a VMSpec leaves CPUCount unset. vmctl(8)
	// itself doesn't expose -c at start time on every OpenBSD release ;
	// we honour it via vm.conf snippets in a follow-up commit. Stored
	// here so the HostInfo reporting + future wiring share the field.
	DefaultCPUs int

	HostUUID string
	Hostname string
}

// Hypervisor implements drivers.HypervisorDriver against vmctl(8).
type Hypervisor struct {
	opts Options
}

// compile-time conformance -- if drivers.HypervisorDriver grows a method,
// the build breaks here, which is exactly the point.
var _ drivers.HypervisorDriver = (*Hypervisor)(nil)

// NewHypervisor constructs the driver, filling in host-derived defaults
// (vmctl on PATH, 512 MiB, 1 vCPU).
func NewHypervisor(o Options) *Hypervisor {
	if o.VmctlBinary == "" {
		o.VmctlBinary = "vmctl"
	}
	if o.DefaultMemMiB == 0 {
		o.DefaultMemMiB = 512
	}
	if o.DefaultCPUs == 0 {
		o.DefaultCPUs = 1
	}
	return &Hypervisor{opts: o}
}

// HostInfo returns the host's identity. All four drivers in this bundle
// share the same HostInfo so the scheduler + audit logs converge.
func (h *Hypervisor) HostInfo(context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(h.opts), nil
}

// CreateVM provisions the VM's static state : its directory, a stable MAC,
// and the name file the rest of the lifecycle hangs off. Idempotent --
// re-creating reuses existing files (matches the weft-driver-qemu contract).
func (h *Hypervisor) CreateVM(_ context.Context, spec drivers.VMSpec) error {
	vmDir := spec.UUID
	if vmDir == "" {
		return fmt.Errorf("vmd CreateVM: empty VM uuid/dir")
	}
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return fmt.Errorf("vmd CreateVM: %w", err)
	}
	if err := ensureMAC(vmDir); err != nil {
		return fmt.Errorf("vmd CreateVM: mac: %w", err)
	}
	// Persist the vmctl VM name so StartVM/StopVM/DeleteVM share one
	// derivation. We use the directory's basename so the operator can
	// correlate `vmctl status` output back to a VM directory.
	if err := ensureName(vmDir, spec.Name); err != nil {
		return fmt.Errorf("vmd CreateVM: name: %w", err)
	}
	return nil
}

// StartVM assembles a `vmctl start ...` invocation from the VM directory
// and execs it. vmctl returns once vmd reports the VM as running ; we
// inherit that contract (no async poll loop needed).
//
// Idempotent : a VM already known to vmd (i.e. `vmctl status` lists it as
// running) is a no-op. We don't probe vmctl status today -- vmctl itself
// returns a non-zero exit when starting an already-running VM, and the
// caller already retries on transient errors. This will sharpen once the
// status probe lands ; the contract is stable.
func (h *Hypervisor) StartVM(_ context.Context, vmUUID string) error {
	vmDir := vmUUID
	name, err := readName(vmDir)
	if err != nil {
		return fmt.Errorf("vmd StartVM: %w", err)
	}
	kernel := filepath.Join(vmDir, "kernel")
	if _, err := os.Stat(kernel); err != nil {
		return fmt.Errorf("vmd StartVM: kernel not found at %s: %w", kernel, err)
	}
	a := startArgs{
		Name:       name,
		Kernel:     kernel,
		MemMiB:     h.opts.DefaultMemMiB,
		LocalIface: true,
	}
	if disk := filepath.Join(vmDir, "disk.img"); fileExists(disk) {
		a.Disks = append(a.Disks, disk)
	}
	args, err := buildStartArgs(a)
	if err != nil {
		return fmt.Errorf("vmd StartVM: %w", err)
	}
	return h.runVmctl(args)
}

// StopVM sends an ACPI shutdown to the guest via `vmctl stop <name>`.
// Idempotent : a missing VM ('vmctl stop' returns non-zero in that case)
// is treated as success. The directory-missing branch is the more common
// idempotence path -- it covers the case where DeleteVM raced StopVM.
func (h *Hypervisor) StopVM(_ context.Context, vmUUID string) error {
	vmDir := vmUUID
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return nil
	}
	name, err := readName(vmDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("vmd StopVM: %w", err)
	}
	args, err := buildStopArgs(name, false)
	if err != nil {
		return fmt.Errorf("vmd StopVM: %w", err)
	}
	// Best-effort : we deliberately swallow vmctl's exit error here so
	// "VM unknown" (idempotence) doesn't surface as a hard failure.
	// Once we probe `vmctl status` we'll distinguish "not found" from
	// "actually running but stop failed" and bubble the latter.
	_ = h.runVmctl(args)
	return nil
}

// DeleteVM force-stops the VM (in case it's still running) and removes
// its state directory. Idempotent.
func (h *Hypervisor) DeleteVM(_ context.Context, vmUUID string) error {
	vmDir := vmUUID
	if name, err := readName(vmDir); err == nil {
		// Force-stop ; ignore the exit status because the VM may already
		// be gone from vmd's table.
		if args, e := buildStopArgs(name, true); e == nil {
			_ = h.runVmctl(args)
		}
	}
	return os.RemoveAll(vmDir)
}

// AttachDisk ensures a backing file exists for the disk (transitional mode,
// matching weft-driver-vz + weft-driver-qemu) : when missing and
// SizeGiB > 0 it execs `vmctl create -s <size> <path>` ; SizeGiB == 0 with
// a missing file is an error. The boot disk is the file named disk.img
// in the VM directory by convention.
func (h *Hypervisor) AttachDisk(_ context.Context, vmUUID string, disk drivers.DiskSpec) error {
	path := disk.BackingPath
	if path == "" {
		path = filepath.Join(vmUUID, "disk.img")
	}
	if fileExists(path) {
		return nil
	}
	if disk.SizeGiB <= 0 {
		return fmt.Errorf("vmd AttachDisk: %s missing and SizeGiB==0", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	args, err := buildCreateDiskArgs(path, disk.SizeGiB)
	if err != nil {
		return fmt.Errorf("vmd AttachDisk: %w", err)
	}
	return h.runVmctl(args)
}

// DetachDisk is a no-op in the transitional file model. vmd binds disks
// at boot time and doesn't support disk hot-unplug -- the file stays on
// disk until DeleteVM (or a future VolumeDriver) reclaims it. Idempotent
// by definition.
func (h *Hypervisor) DetachDisk(context.Context, string, string) error { return nil }

// AttachNIC / DetachNIC are unsupported : the vmd driver wires NICs at
// StartVM time via the `-L` flag (a local virtio interface auto-bridged
// to the host's switch), not via hot-plug. Same call-site convention as
// weft-driver-qemu's user-mode networking -- the Network driver returns
// ErrUnsupported here so callers get a clear "patch welcome" signal.
func (h *Hypervisor) AttachNIC(context.Context, string, drivers.NICHandle) error {
	return drivers.ErrUnsupported
}
func (h *Hypervisor) DetachNIC(context.Context, string, string) error {
	return drivers.ErrUnsupported
}

// runVmctl is the single seam every shell-out goes through. Kept on the
// receiver so we can in a follow-up swap it for a fake in unit tests
// without touching every caller. Stdout/stderr are wired to the parent's
// streams so vmctl's error messages land in the agent log.
func (h *Hypervisor) runVmctl(args []string) error {
	bin := h.opts.VmctlBinary
	if bin == "" {
		bin = "vmctl"
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vmctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// ----- helpers ----------------------------------------------------------

// ensureMAC writes a stable locally-administered MAC into <vmDir>/mac.txt
// the first time the VM is created. Subsequent CreateVM calls reuse it,
// which is what the idempotence contract requires.
func ensureMAC(vmDir string) error {
	p := filepath.Join(vmDir, "mac.txt")
	if fileExists(p) {
		return nil
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	b[0] = (b[0] | 0x02) & 0xfe // locally administered, unicast
	mac := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
	return os.WriteFile(p, []byte(mac), 0o600)
}

// ensureName persists the vmctl VM name. If the spec leaves Name empty we
// fall back to the directory's basename so `vmctl status` output is at
// least debuggable.
func ensureName(vmDir, specName string) error {
	p := filepath.Join(vmDir, "name")
	if fileExists(p) {
		return nil
	}
	n := specName
	if n == "" {
		n = filepath.Base(vmDir)
	}
	return os.WriteFile(p, []byte(n), 0o600)
}

// readName returns the persisted vmctl name. Used by every method that
// shells out to vmctl so one CreateVM-time decision drives the rest of
// the lifecycle.
func readName(vmDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(vmDir, "name"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// driverArch normalises GOARCH into the labels openweft uses everywhere
// (arm64 / amd64), so HostInfo.Architecture is comparable across drivers.
func driverArch(goarch string) string {
	switch goarch {
	case "arm64", "amd64", "riscv64", "loongarch64":
		return goarch
	default:
		return goarch
	}
}

// hostArchForGOARCH returns the canonical openweft arch label for the
// running binary. Wrapped so tests don't need to set GOARCH.
func hostArchForGOARCH() string { return driverArch(runtime.GOARCH) }
