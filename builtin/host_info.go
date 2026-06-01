package builtin

import (
	drivers "github.com/openweft/weft-drivers"
)

// hostInfoFor builds the HostInfo all four vmd drivers report. It mirrors
// the weft-driver-qemu helper so the scheduler sees a consistent shape
// across hypervisor backends (UUID, hostname, hypervisor tag, arch label).
//
// Hypervisor is reported as "vmd" -- not "openbsd-vmd" -- because the
// existing scheduler labels in weft/cluster already speak in pure
// hypervisor names ("apple-vz", "qemu-kvm", "qemu-tcg"). "vmd" slots in
// alongside without a special case.
func hostInfoFor(o Options) drivers.HostInfo {
	return drivers.HostInfo{
		UUID:         o.HostUUID,
		Hostname:     o.Hostname,
		Hypervisor:   "vmd",
		Architecture: hostArchForGOARCH(),
	}
}
