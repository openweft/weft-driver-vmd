// Command weft-driver-vmd is the OpenBSD vmd(8) hypervisor driver as an
// external weft go-plugin. It is the third sibling in the weft-driver-*
// family : weft-driver-vz pilots Apple's Virtualization framework on macOS
// (cgo + objc bindings), weft-driver-qemu shells out to qemu-system on
// Linux (pure Go, no cgo), and this one shells out to vmctl(8) on OpenBSD.
//
// Like weft-driver-qemu, the binary is pure Go (CGO_ENABLED=0 everywhere)
// because it does not link the hypervisor in-process -- it just exec's
// vmctl with the right argument list. That keeps the cross-build matrix
// trivial : the same source tree builds on darwin (developer host),
// openbsd (the real runtime) and linux (CI sanity).
//
// Cobra is the CLI surface even though the binary has a single mode
// (launched by weft-agent with no arguments, it serves the four driver
// services over go-plugin). The convention exists across every openweft
// binary so the operator UX -- --version, --help -- is uniform ; see
// [[feedback-cli-cobra]] in the project memory.
//
// Launch-time tuning arrives via environment variables (set by the
// launching weft-agent process) :
//
//   WEFT_DRIVER_HOST_UUID, WEFT_DRIVER_HOSTNAME, WEFT_DRIVER_STATE_DIR
//       Shared across every weft-driver-*. Defined in weft-driver-plugin.
//
//   WEFT_VMCTL_BINARY        : path to vmctl(8). Defaults to "vmctl" on PATH.
//                              Set to "doas vmctl" when the driver is not
//                              already running as root and the host sudoers/
//                              doas.conf grants the operator vmctl access.
//   WEFT_VMD_DEFAULT_MEM_MIB : fallback memory when VMSpec.MemoryMiB == 0.
//   WEFT_VMD_DEFAULT_CPUS    : fallback CPU count when VMSpec.CPUCount == 0.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	vmddriver "github.com/openweft/weft-driver-vmd/builtin"
	weftplugin "github.com/openweft/weft-driver-plugin"
)

// Build-time stamps populated via -ldflags "-X main.version=...". Same scheme
// the other openweft binaries use so the operator gets the same UX across
// daemons.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// envInt reads an int env var, returning 0 when unset or malformed so the
// driver falls back to its built-in default. Mirrors the same helper in
// weft-driver-qemu so the env-tuning UX matches.
func envInt(name string) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// Optional vmd tuning passed through from the host. Empty values keep the
// driver's defaults (vmctl on PATH, 512 MiB, 1 vCPU).
const (
	envVmctlBinary = "WEFT_VMCTL_BINARY"
	envVmdMemMiB   = "WEFT_VMD_DEFAULT_MEM_MIB"
	envVmdCPUs     = "WEFT_VMD_DEFAULT_CPUS"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRootCmd wires the single-mode cobra root. The Run hook is what
// weft-agent actually drives -- it spawns the binary, the binary calls
// weftplugin.Serve, and Serve blocks until the host disconnects.
func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "weft-driver-vmd",
		Short: "OpenBSD vmd(8) hypervisor driver for openweft (go-plugin)",
		Long: `weft-driver-vmd is the openweft hypervisor driver for OpenBSD's vmd(8).
It is launched by weft-agent as a HashiCorp go-plugin subprocess and
serves the four driver services (Hypervisor, Network, Volume, Image) over
gRPC on stdin/stdout. The driver does not run vmd in-process : it execs
vmctl(8) with arguments derived from the VMSpec.`,
		SilenceUsage: true,
		Version:      fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		Run: func(*cobra.Command, []string) {
			serve()
		},
	}
}

// serve constructs the driver bundle and hands it to weftplugin.Serve.
// Kept separate from the cobra wiring so tests can exercise the construction
// path without launching the plugin handshake.
func serve() {
	b := vmddriver.New(vmddriver.BundleOptions{
		Options: vmddriver.Options{
			HostUUID:      os.Getenv(weftplugin.EnvHostUUID),
			Hostname:      os.Getenv(weftplugin.EnvHostname),
			VmctlBinary:   os.Getenv(envVmctlBinary),
			DefaultMemMiB: envInt(envVmdMemMiB),
			DefaultCPUs:   envInt(envVmdCPUs),
		},
		StateDir: os.Getenv(weftplugin.EnvStateDir),
	})
	weftplugin.Serve(weftplugin.DriverSet{
		Hypervisor: b.Hypervisor,
		Network:    b.Network,
		Volume:     b.Volume,
		Image:      b.Image,
	})
}
