// image.go — ImageDriver scaffold for vmd. Images are pulled from an
// OCI registry, unpacked, and converted to raw disk files under
// StateDir/images/. vmd consumes raw disks at boot through the -d
// flag of `vmctl start`.

package builtin

import (
	"context"
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

type vmdImage struct {
	opts Options
	log  *slog.Logger
}

func newVmdImage(opts Options) *vmdImage {
	return &vmdImage{opts: opts, log: opts.Logger}
}

func (i *vmdImage) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{UUID: i.opts.HostUUID, Hostname: i.opts.Hostname}, nil
}

func (i *vmdImage) Pull(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *vmdImage) LocalPath(ctx context.Context, ref string) (string, error) {
	return "", drivers.ErrUnsupported
}

func (i *vmdImage) Delete(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *vmdImage) InCache(ctx context.Context, ref string) (bool, error) {
	return false, drivers.ErrUnsupported
}
