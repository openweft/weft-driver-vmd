// volume.go — VolumeDriver implementation for vmd. Disks are flat raw
// files under StateDir/disks/<uuid>.img. The driver shells out to
// `vmctl create <path> -s <size>` to provision the file ; deletion is
// a plain unlink. Snapshots / backups route through weft-block when
// cluster-replicated semantics are needed — they stay ErrUnsupported
// at this layer.

package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	drivers "github.com/openweft/weft-drivers"
)

type vmdVolume struct {
	opts Options
	log  *slog.Logger
}

func newVmdVolume(opts Options) *vmdVolume {
	return &vmdVolume{opts: opts, log: opts.Logger}
}

func (v *vmdVolume) Name() string { return "vmd" }

// Local is true : vmd disks live on the host's filesystem under
// StateDir/disks/. Cluster-wide block storage routes through
// weft-block (Name="block") via the dispatch table.
func (v *vmdVolume) Local() bool { return true }

func (v *vmdVolume) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{UUID: v.opts.HostUUID, Hostname: v.opts.Hostname}, nil
}

func (v *vmdVolume) diskPath(uuid string) string {
	return filepath.Join(v.opts.StateDir, "disks", uuid+".img")
}

// EnsureVolume provisions a raw disk image at <StateDir>/disks/<uuid>.img.
// Idempotent : an existing file at the same size is a no-op. The
// grow-only contract is enforced — a smaller spec.SizeGiB rejects.
func (v *vmdVolume) EnsureVolume(ctx context.Context, spec drivers.VolumeSpec) error {
	if spec.UUID == "" {
		return errors.New("EnsureVolume: empty uuid")
	}
	if spec.SizeGiB <= 0 {
		return fmt.Errorf("EnsureVolume: size_gib must be > 0 (got %d)", spec.SizeGiB)
	}
	path := v.diskPath(spec.UUID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	// Check for existing — if the file is already at or above the
	// requested size, treat as success. Smaller existing files would
	// silently shrink the volume, so we reject.
	if info, err := os.Stat(path); err == nil {
		want := int64(spec.SizeGiB) * 1024 * 1024 * 1024
		switch {
		case info.Size() == want:
			return nil
		case info.Size() > want:
			return fmt.Errorf("EnsureVolume: existing disk %s is %d bytes ; refusing to shrink to %d", path, info.Size(), want)
		default:
			// Grow path : extend the file via truncate. vmctl create
			// won't re-provision an existing file, so we handle the
			// growth ourselves.
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return fmt.Errorf("open %s for grow: %w", path, err)
			}
			defer f.Close()
			if err := f.Truncate(want); err != nil {
				return fmt.Errorf("truncate %s to %d: %w", path, want, err)
			}
			v.log.Info("vmd EnsureVolume: grew disk", "uuid", spec.UUID, "old", info.Size(), "new", want)
			return nil
		}
	}
	// Fresh provision : create the raw image file ourselves. vmd
	// treats truncated files as flat block devices ; no qcow2 header
	// magic needed for the raw format. See imsg_vmd.go's
	// createDiskRaw for the implementation rationale.
	want := int64(spec.SizeGiB) * 1024 * 1024 * 1024
	if err := createDiskRaw(path, want); err != nil {
		return err
	}
	v.log.Info("vmd EnsureVolume: created", "uuid", spec.UUID, "path", path, "size_gib", spec.SizeGiB)
	return nil
}

// DestroyVolume removes the backing file. Idempotent — a missing file
// is success.
func (v *vmdVolume) DestroyVolume(ctx context.Context, volumeUUID string) error {
	path := v.diskPath(volumeUUID)
	err := os.Remove(path)
	if err == nil {
		v.log.Info("vmd DestroyVolume: removed", "uuid", volumeUUID)
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("rm %s: %w", path, err)
}

// AttachVolume returns the local disk path. Local() drivers don't
// have a "stage on host A then move to host B" concept — the file is
// already where the hypervisor expects it, the BackingPath is the
// addressing key.
func (v *vmdVolume) AttachVolume(ctx context.Context, volumeUUID, hostUUID string) (drivers.AttachedVolume, error) {
	path := v.diskPath(volumeUUID)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return drivers.AttachedVolume{}, drivers.ErrNotFound
		}
		return drivers.AttachedVolume{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return drivers.AttachedVolume{
		BackingPath: path,
	}, nil
}

// DetachVolume is a no-op for the file-backed Local driver — the file
// stays where it is. The next AttachVolume will pick it up again.
// This matches qemu's file backend semantics.
func (v *vmdVolume) DetachVolume(ctx context.Context, volumeUUID, hostUUID string) error {
	return nil
}

// Snapshot / backup operations route through weft-block when the
// operator wants cluster-replicated semantics. Local file backends
// don't carry weft's snapshot/backup contract.
func (v *vmdVolume) CreateSnapshot(ctx context.Context, spec drivers.SnapshotSpec) (drivers.Snapshot, error) {
	return drivers.Snapshot{}, drivers.ErrUnsupported
}

func (v *vmdVolume) ListSnapshots(ctx context.Context, volumeUUID string) ([]drivers.Snapshot, error) {
	return nil, drivers.ErrUnsupported
}

func (v *vmdVolume) DeleteSnapshot(ctx context.Context, volumeUUID, snapshotName string) error {
	return drivers.ErrUnsupported
}

func (v *vmdVolume) RevertSnapshot(ctx context.Context, volumeUUID, snapshotName string) error {
	return drivers.ErrUnsupported
}

func (v *vmdVolume) CreateBackup(ctx context.Context, spec drivers.BackupSpec) (drivers.Backup, error) {
	return drivers.Backup{}, drivers.ErrUnsupported
}

func (v *vmdVolume) ListBackups(ctx context.Context, target, volumeUUID string) ([]drivers.Backup, error) {
	return nil, drivers.ErrUnsupported
}

func (v *vmdVolume) DeleteBackup(ctx context.Context, backupURL string) error {
	return drivers.ErrUnsupported
}

func (v *vmdVolume) RestoreBackup(ctx context.Context, backupURL string, spec drivers.VolumeSpec) error {
	return drivers.ErrUnsupported
}
