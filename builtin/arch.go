package builtin

import "runtime"

// driverArch normalises GOARCH into the labels openweft uses everywhere
// (arm64 / amd64 / riscv64 / loongarch64), so HostInfo.Architecture is
// comparable across drivers.
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
