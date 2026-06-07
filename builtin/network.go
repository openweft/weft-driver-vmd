// network.go — NetworkDriver scaffold for vmd. OpenBSD's networking
// model uses vether interfaces as virtual switches and tap interfaces
// per-VM. The driver provisions both via `ifconfig` and binds them at
// `vmctl start` time via the -n switch flag.

package builtin

import (
	"context"
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

type vmdNetwork struct {
	opts Options
	log  *slog.Logger
}

func newVmdNetwork(opts Options) *vmdNetwork {
	return &vmdNetwork{opts: opts, log: opts.Logger}
}

func (n *vmdNetwork) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{UUID: n.opts.HostUUID, Hostname: n.opts.Hostname}, nil
}

// EnsureNetwork creates a vether(4) interface for the network's
// virtual switch — `ifconfig vether<N> create` + IP bind to the
// gateway. Idempotent : re-running with the same NetworkSpec is a
// no-op (vether already exists, IP already bound).
func (n *vmdNetwork) EnsureNetwork(ctx context.Context, spec drivers.NetworkSpec) error {
	return drivers.ErrUnsupported
}

func (n *vmdNetwork) DestroyNetwork(ctx context.Context, networkUUID string) error {
	return drivers.ErrUnsupported
}

// AttachPort provisions a tap(4) interface for the VM and returns its
// device name as the NICHandle. vmd binds it at VM start via the
// per-VM tap allocation.
func (n *vmdNetwork) AttachPort(ctx context.Context, spec drivers.PortSpec) (drivers.NICHandle, error) {
	return drivers.NICHandle{}, drivers.ErrUnsupported
}

func (n *vmdNetwork) DetachPort(ctx context.Context, portUUID string) error {
	return drivers.ErrUnsupported
}

// RotateMeshPeer : WireGuard mesh peers run as overlays managed by
// weft-network's mesh microVMs, not by the host hypervisor driver.
// Returning ErrNotApplicable surfaces that intent explicitly.
func (n *vmdNetwork) RotateMeshPeer(ctx context.Context, spec drivers.PortSpec) error {
	return drivers.ErrNotApplicable
}
