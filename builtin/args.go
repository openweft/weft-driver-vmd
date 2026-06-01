package builtin

import (
	"fmt"
	"strconv"
)

// args.go is the pure -- table-testable -- vmctl(8) argument builder. It
// has no I/O and no exec ; vmd.go consumes the []string returned here and
// hands it to os/exec.Command. Keeping the argv assembly pure means the
// wiring is unit-testable from any host, including the macOS dev box where
// vmctl doesn't exist.
//
// vmctl(8)'s relevant verbs :
//
//   vmctl create -s <size> <path>          # create a sparse disk image
//   vmctl start  <name> [opts...]          # boot a VM
//   vmctl stop   [-f] <name>               # ACPI shutdown (or force-kill)
//   vmctl status [<name>]                  # list / single-VM status
//
// The start argv supports :
//   -k <kernel>      direct-Linux/OpenBSD kernel image
//   -d <disk>        disk image (repeatable)
//   -m <memory>      memory (e.g. 512M, 2G)
//   -L               attach a local interface (auto-bridged to the host pf)
//   -i <count>       NIC count (alt to -L for multiple)
//   -r <iso>         boot ISO (we don't surface this in the scaffold ; vmd
//                    only does direct-kernel-boot for our microVM model)
//   -c               attach to console after start (we never want this in
//                    daemon mode -- always run detached)
//
// startArgs distils the bits of a VM spec the driver needs at boot time
// into one struct so the builder stays a single deterministic function.
type startArgs struct {
	// Name is the unique vmd VM identifier. vmd's namespace is flat -- we
	// use the VMSpec.UUID's basename so a 'vmctl status' output is
	// debuggable.
	Name string
	// Kernel is the absolute host path to the guest kernel image.
	Kernel string
	// Disks is the ordered list of disk image paths. The first one is the
	// boot disk by convention.
	Disks []string
	// MemMiB is the resolved memory size in MiB. The builder formats this
	// into vmctl's <number>M syntax.
	MemMiB int
	// LocalIface (-L) attaches a virtio NIC bridged to the host's default
	// switch. The mesh / SG layer is pf(4)'s job on the host ; the
	// scaffold sets this true by default, the Network driver overrides it
	// once we wire a real attach path.
	LocalIface bool
}

// buildStartArgs renders the argv (excluding argv[0] -- vmctl) for a
// "vmctl start" invocation. Pure / deterministic so it's table-testable
// without vmctl installed.
//
// vmctl's start command insists on -k for direct-kernel boot ; we error
// out early if it's missing so the caller gets a useful message instead
// of vmctl's terse "missing kernel argument".
func buildStartArgs(a startArgs) ([]string, error) {
	if a.Name == "" {
		return nil, fmt.Errorf("vmd: VM name is required")
	}
	if a.Kernel == "" {
		return nil, fmt.Errorf("vmd: kernel is required (direct_linux boot)")
	}
	mem := a.MemMiB
	if mem < 1 {
		mem = 512
	}

	args := []string{
		"start", a.Name,
		"-k", a.Kernel,
		"-m", strconv.Itoa(mem) + "M",
	}
	for _, d := range a.Disks {
		args = append(args, "-d", d)
	}
	if a.LocalIface {
		args = append(args, "-L")
	}
	return args, nil
}

// buildStopArgs renders the argv for "vmctl stop". Force is true for
// DeleteVM (we want the VM dead before we tear down its state directory) ;
// false for the polite StopVM path which sends an ACPI shutdown so the
// guest gets a chance to sync its filesystems.
func buildStopArgs(name string, force bool) ([]string, error) {
	if name == "" {
		return nil, fmt.Errorf("vmd: VM name is required")
	}
	args := []string{"stop"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	return args, nil
}

// buildCreateDiskArgs renders the argv for "vmctl create -s <size> <path>".
// vmctl creates a sparse qcow2-like image (actually a raw sparse file ;
// vmd does its own snapshot format on top).
//
// sizeGiB == 0 is rejected ; the disk has to be sized at creation time and
// vmctl doesn't have a "match an existing file" mode.
func buildCreateDiskArgs(path string, sizeGiB int) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("vmd: disk path is required")
	}
	if sizeGiB < 1 {
		return nil, fmt.Errorf("vmd: disk size must be >= 1 GiB (got %d)", sizeGiB)
	}
	return []string{"create", "-s", strconv.Itoa(sizeGiB) + "G", path}, nil
}
