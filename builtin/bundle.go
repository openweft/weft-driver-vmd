package builtin

// bundle.go assembles the four driver instances a weft agent needs to run on
// an OpenBSD vmd host -- mirroring weft-driver-vz's Bundle and
// weft-driver-qemu's Bundle so the agent's dispatch wiring is identical
// regardless of backend.
//
// Unlike the vz bundle (cgo + AppKit) this one is cross-platform (pure Go,
// no cgo), so a weft agent can compile the driver on a darwin or linux
// host -- the binary just won't be useful until it lands on OpenBSD where
// vmctl(8) exists.

import "path/filepath"

// Bundle holds the four vmd-host driver instances.
type Bundle struct {
	Hypervisor *Hypervisor
	Network    *Network
	Volume     *Volume
	Image      *Image
}

// BundleOptions wraps construction inputs for all four drivers. StateDir is
// the on-host root for per-volume + image-cache directories ; on OpenBSD
// production hosts that's typically /var/db/weft.
type BundleOptions struct {
	Options
	StateDir string
}

// New returns the driver bundle for one vmd host ; all four drivers share
// the same HostInfo and so see the same hypervisor + arch + uuid.
func New(o BundleOptions) *Bundle {
	stateDir := o.StateDir
	if stateDir == "" {
		stateDir = ".weft-agent"
	}
	return &Bundle{
		Hypervisor: NewHypervisor(o.Options),
		Network:    NewNetwork(o.Options),
		Volume:     NewVolume(VolumeOptions{Options: o.Options, StateDir: filepath.Join(stateDir, "volumes")}),
		Image:      NewImage(ImageOptions{Options: o.Options, CacheDir: filepath.Join(stateDir, "cache")}),
	}
}
