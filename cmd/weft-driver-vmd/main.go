// Command weft-driver-vmd is the OpenBSD vmd(8) hypervisor driver as
// an external weft go-plugin. The plugin handshake is in place ; the
// vmctl integration is a follow-on (every driver method returns
// drivers.ErrUnsupported until that ships — see the README).
//
// Launched by weft with no arguments, it serves the four driver services
// over go-plugin gRPC. Launch-time config arrives via env, set by the
// launching weft-agent when the operator points `drivers { vmd = ... }`
// at this binary in cluster.hcl.
//
// Platform : OpenBSD only at runtime (vmctl(8) is OpenBSD-native). The
// binary cross-compiles on macOS/Linux for vet purposes ; running it
// off-OpenBSD just means every method returns ErrUnsupported with a
// "vmctl not present" log line on the first call.
package main

import (
	"log/slog"
	"os"

	weftplugin "github.com/openweft/weft-driver-plugin"
	vmddriver "github.com/openweft/weft-driver-vmd/builtin"
)

// Env vars the host passes through. None are required ; the defaults
// match an out-of-the-box OpenBSD installation (vmctl in /usr/sbin,
// vmd socket at /var/run/vmd.sock, state under /var/lib/weft/vmd).
const (
	envVmctlBinary = "WEFT_VMD_VMCTL_BINARY"
	envVmdSocket   = "WEFT_VMD_SOCKET"
	envSwitchName  = "WEFT_VMD_SWITCH"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	b, err := vmddriver.NewBundle(vmddriver.Options{
		HostUUID:    os.Getenv(weftplugin.EnvHostUUID),
		Hostname:    os.Getenv(weftplugin.EnvHostname),
		VmctlBinary: os.Getenv(envVmctlBinary),
		VmdSocket:   os.Getenv(envVmdSocket),
		StateDir:    os.Getenv(weftplugin.EnvStateDir),
		SwitchName:  os.Getenv(envSwitchName),
		Logger:      logger,
	})
	if err != nil {
		logger.Error("weft-driver-vmd : bundle init failed", "err", err)
		os.Exit(1)
	}
	weftplugin.Serve(weftplugin.DriverSet{
		Hypervisor: b.Hypervisor,
		Network:    b.Network,
		Volume:     b.Volume,
		Image:      b.Image,
	})
}
