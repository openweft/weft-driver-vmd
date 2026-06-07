// Package builtin assembles the four driver interfaces a HostHandle expects
// (Hypervisor, Volume, Network, Image) into one Bundle the cmd entrypoint
// hands to the plugin gRPC server.
//
// OpenBSD vmd(8) backend. The driver shells out to vmctl(8) for VM
// lifecycle and reads vmd's machine-readable status to drive observability.
// See the package README for status — most methods return
// drivers.ErrUnsupported today ; the integration drops in one method
// at a time without rewiring the plumbing.
package builtin

import (
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

// Bundle is the all-in-one driver bundle the plugin entrypoint serves.
type Bundle struct {
	Hypervisor drivers.HypervisorDriver
	Volume     drivers.VolumeDriver
	Network    drivers.NetworkDriver
	Image      drivers.ImageDriver
}

// Options configures one vmd backend instance.
type Options struct {
	// HostUUID is the UUID weft-agent stamped on this host. Surfaced
	// through HostInfo so the dispatch table can route work back here.
	HostUUID string
	// Hostname is the cosmetic name (operator-visible in CLI listings).
	Hostname string
	// VmctlBinary is the path to vmctl(8). Empty defaults to "vmctl"
	// on $PATH — OpenBSD ships it in /usr/sbin which is in admin's PATH.
	VmctlBinary string
	// VmdSocket is the path to vmd's imsg socket. Default
	// /var/run/vmd.sock. Unused by the v0.1 vmctl path ; reserved for
	// the imsg milestone.
	VmdSocket string
	// StateDir is where the driver keeps per-VM bookkeeping (PIDfile,
	// rendered vm.conf fragments, disk images by default). Defaults to
	// /var/lib/weft/vmd/ ; the agent typically overrides this via
	// the WEFT_STATE_DIR env passed at plugin launch.
	StateDir string
	// SwitchName is the default vmd switch (vether interface) workloads
	// attach to when a NetworkSpec doesn't pin one. vmd ships a default
	// "uplink" switch in vm.conf ; if the operator hasn't created one,
	// leave this empty and EnsureNetwork will provision per-VM tap-on-
	// bridge instead.
	SwitchName string
	// Logger : injected by main.go so the driver shares the agent's
	// slog handler (level / json toggle propagate).
	Logger *slog.Logger
}

// NewBundle returns a Bundle wiring all four drivers against the same
// vmd backend. Construction is cheap ; the actual vmctl probe happens
// lazily on the first HypervisorDriver call so the plugin handshake
// completes even when vmctl is mid-restart.
func NewBundle(opts Options) (*Bundle, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.VmctlBinary == "" {
		opts.VmctlBinary = "vmctl"
	}
	if opts.VmdSocket == "" {
		opts.VmdSocket = "/var/run/vmd.sock"
	}
	if opts.StateDir == "" {
		opts.StateDir = "/var/lib/weft/vmd"
	}
	hv, err := newVmdHypervisor(opts)
	if err != nil {
		return nil, err
	}
	return &Bundle{
		Hypervisor: hv,
		Volume:     newVmdVolume(opts),
		Network:    newVmdNetwork(opts),
		Image:      newVmdImage(opts),
	}, nil
}
