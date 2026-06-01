package builtin

// network.go is the vmd NetworkDriver scaffold. vmd integrates with the
// host's pf(4) firewall for the data path : `vmctl start -L` creates a
// local virtio interface that vmd auto-bridges to the default switch
// declared in /etc/vm.conf. There's no driver-side host construct to
// create today, so every dynamic method returns ErrUnsupported until
// the pf(4) + switch.conf wiring lands.
//
// When that wiring arrives this is where it goes : EnsureNetwork would
// templated-write a switch{} block into /etc/vm.conf and reload vmd ;
// AttachPort would mutate the per-VM vm.conf interface stanza ; mesh
// support shells out to wg(8) on the OpenBSD host (the wireguard kernel
// module exists upstream).

import (
	"context"

	drivers "github.com/openweft/weft-drivers"
)

// Network implements drivers.NetworkDriver for vmd hosts.
type Network struct {
	opts Options
}

func NewNetwork(o Options) *Network { return &Network{opts: o} }

// compile-time conformance.
var _ drivers.NetworkDriver = (*Network)(nil)

func (n *Network) HostInfo(context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(n.opts), nil
}
func (n *Network) EnsureNetwork(context.Context, drivers.NetworkSpec) error {
	return drivers.ErrUnsupported
}
func (n *Network) DestroyNetwork(context.Context, string) error { return drivers.ErrUnsupported }
func (n *Network) AttachPort(context.Context, drivers.PortSpec) (drivers.NICHandle, error) {
	return drivers.NICHandle{}, drivers.ErrUnsupported
}
func (n *Network) DetachPort(context.Context, string) error { return drivers.ErrUnsupported }
func (n *Network) RotateMeshPeer(context.Context, drivers.PortSpec) error {
	return drivers.ErrUnsupported
}
