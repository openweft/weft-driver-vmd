// vmd.go — HypervisorDriver implementation. The transport is OpenBSD's
// native imsg(3) protocol over /var/run/vmd.sock — no vmctl(8) exec,
// no text parsing. See imsg.go + imsg_vmd.go for the wire shapes.
//
// The lifecycle binds 1:1 to vmd messages :
//
//   CreateVM   : reserve per-VM state under StateDir. No vmd-side
//                provisioning here ; vmd creates the VM when StartVM
//                fires IMSG_VMDOP_START_VM_REQUEST. The spec is
//                persisted as JSON at StateDir/<uuid>/spec.json so
//                Attach* / StartVM can rebuild the start payload.
//   StartVM    : load spec, build vmStartParams, send the imsg, wait
//                for the start-response confirmation.
//   StopVM     : graceful terminate (force=false) escalating to
//                force=true on ctx deadline. Polls getInfoVMs until
//                the VM disappears or reports "stopped".
//   DeleteVM   : force-terminate + remove StateDir.
//   AttachDisk : persist into spec ; takes effect on next StartVM
//                (vmd doesn't support disk hotplug).
//   AttachNIC  : persist into spec ; same hotplug story.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	drivers "github.com/openweft/weft-drivers"
)

type vmdHypervisor struct {
	opts Options
	log  *slog.Logger
}

func newVmdHypervisor(opts Options) (*vmdHypervisor, error) {
	return &vmdHypervisor{opts: opts, log: opts.Logger}, nil
}

// HostInfo identifies this driver to the dispatch table. vmd is a
// per-host hypervisor (one vmd(8) instance per OpenBSD host).
// Routed through hostInfoFor so Hypervisor + Architecture stay
// consistent with the qemu/vz/dcs siblings ; the scheduler reads
// those fields by exact string.
func (h *vmdHypervisor) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(h.opts), nil
}

// dial opens a fresh imsg connection to vmd. We don't pool connections
// — vmd's protocol is request/response per connection, and the
// dial+TLS handshake cost on a Unix socket is sub-millisecond. The
// caller defers .close() on the returned client.
func (h *vmdHypervisor) dial() (*imsgClient, error) {
	return dialImsg(h.opts.VmdSocket)
}

// vmState is the persisted per-VM spec the driver re-reads at every
// lifecycle call. Lives at StateDir/<uuid>/spec.json.
type vmState struct {
	UUID    string   `json:"uuid"`
	Name    string   `json:"name"`
	MemMiB  int      `json:"mem_mib"`
	CPUs    int      `json:"cpus"`
	Boot    string   `json:"boot,omitempty"` // kernel path, empty = vmd default ("/bsd")
	Disks   []string `json:"disks"`          // ordered, first = root
	NICs    []string `json:"nics"`           // ordered, switch names
}

func (h *vmdHypervisor) vmDir(uuid string) string {
	return filepath.Join(h.opts.StateDir, uuid)
}

func (h *vmdHypervisor) specPath(uuid string) string {
	return filepath.Join(h.vmDir(uuid), "spec.json")
}

func (h *vmdHypervisor) loadSpec(uuid string) (*vmState, error) {
	b, err := os.ReadFile(h.specPath(uuid))
	if err != nil {
		return nil, err
	}
	var s vmState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode spec %s: %w", uuid, err)
	}
	return &s, nil
}

func (h *vmdHypervisor) saveSpec(s *vmState) error {
	dir := h.vmDir(s.UUID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode spec: %w", err)
	}
	tmp := h.specPath(s.UUID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return fmt.Errorf("write tmp spec: %w", err)
	}
	if err := os.Rename(tmp, h.specPath(s.UUID)); err != nil {
		return fmt.Errorf("rename spec: %w", err)
	}
	return nil
}

// vmName derives the vmd-side name (≤63 chars + NUL) from a weft UUID.
// vmd's VMM_MAX_NAME_LEN is 64.
func vmName(uuid string) string {
	const prefix = "weft-"
	if len(uuid) <= 63-len(prefix) {
		return prefix + uuid
	}
	return prefix + uuid[:63-len(prefix)]
}

// CreateVM stamps the per-VM spec. vmd has no separate "create" state ;
// the spec lives on the host until StartVM fires the start-imsg.
// Re-running CreateVM with the same spec is idempotent.
func (h *vmdHypervisor) CreateVM(ctx context.Context, spec drivers.VMSpec) error {
	if spec.UUID == "" {
		return errors.New("CreateVM: empty uuid")
	}
	s := &vmState{
		UUID:   spec.UUID,
		Name:   vmName(spec.UUID),
		MemMiB: spec.MemoryMiB,
		CPUs:   spec.CPUCount,
		Boot:   spec.BootRef,
	}
	if err := h.saveSpec(s); err != nil {
		return err
	}
	h.log.Info("vmd CreateVM: spec persisted", "uuid", spec.UUID, "name", s.Name)
	return nil
}

// StartVM rebuilds the start payload from the persisted spec, sends
// IMSG_VMDOP_START_VM_REQUEST, waits for the success response. vmd's
// start is sync from the protocol's POV — the response carries the
// VM ID, which we discard (the name is our addressing key).
func (h *vmdHypervisor) StartVM(ctx context.Context, vmUUID string) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		return fmt.Errorf("StartVM: %w", err)
	}
	c, err := h.dial()
	if err != nil {
		return fmt.Errorf("StartVM: %w", err)
	}
	defer c.close()
	vmid, err := c.startVM(ctx, vmStartParams{
		Name:       s.Name,
		MemMiB:     s.MemMiB,
		VCPUs:      s.CPUs,
		BootKernel: s.Boot,
		Disks:      s.Disks,
		Nics:       s.NICs,
	})
	if err != nil {
		return fmt.Errorf("vmd startVM: %w", err)
	}
	h.log.Info("vmd StartVM: started", "name", s.Name, "vmid", vmid)
	return nil
}

// StopVM tries a graceful terminate first ; the caller's ctx deadline
// triggers escalation to force=true. After the call, poll
// getInfoVMs() until the VM disappears or reaches state "stopped".
func (h *vmdHypervisor) StopVM(ctx context.Context, vmUUID string) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("StopVM: %w", err)
	}
	c, err := h.dial()
	if err != nil {
		return fmt.Errorf("StopVM: %w", err)
	}
	defer c.close()
	info, err := c.findVM(ctx, s.Name)
	if errors.Is(err, ErrVMNotFound) {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("StopVM: lookup: %w", err)
	}
	// Graceful terminate.
	if err := c.terminateVM(ctx, info.ID, s.Name, false); err != nil {
		return fmt.Errorf("vmd terminate: %w", err)
	}
	// Wait for stop ; escalate to force on ctx deadline.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, err := c.findVM(ctx, s.Name)
		if errors.Is(err, ErrVMNotFound) {
			return nil
		}
		if err == nil && got.State == "stopped" {
			return nil
		}
		select {
		case <-ctx.Done():
			_ = c.terminateVM(context.Background(), info.ID, s.Name, true)
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	h.log.Warn("vmd StopVM: escalating to force-terminate", "name", s.Name)
	return c.terminateVM(ctx, info.ID, s.Name, true)
}

// DeleteVM force-terminates the VM and removes its state directory.
// Idempotent : missing VM / missing dir both return nil.
func (h *vmdHypervisor) DeleteVM(ctx context.Context, vmUUID string) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("DeleteVM: %w", err)
	}
	c, err := h.dial()
	if err == nil {
		defer c.close()
		if info, err := c.findVM(ctx, s.Name); err == nil {
			_ = c.terminateVM(ctx, info.ID, s.Name, true)
		}
	}
	if err := os.RemoveAll(h.vmDir(vmUUID)); err != nil {
		return fmt.Errorf("rm %s: %w", h.vmDir(vmUUID), err)
	}
	h.log.Info("vmd DeleteVM: removed", "uuid", vmUUID)
	return nil
}

// AttachDisk records the disk path in the persisted spec. vmd has no
// disk-hotplug imsg ; the binding takes effect on the next StartVM.
func (h *vmdHypervisor) AttachDisk(ctx context.Context, vmUUID string, disk drivers.DiskSpec) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		return fmt.Errorf("AttachDisk: %w", err)
	}
	for _, d := range s.Disks {
		if d == disk.BackingPath {
			return nil
		}
	}
	s.Disks = append(s.Disks, disk.BackingPath)
	return h.saveSpec(s)
}

func (h *vmdHypervisor) DetachDisk(ctx context.Context, vmUUID, volumeUUID string) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		return fmt.Errorf("DetachDisk: %w", err)
	}
	out := make([]string, 0, len(s.Disks))
	for _, d := range s.Disks {
		if d != volumeUUID {
			out = append(out, d)
		}
	}
	s.Disks = out
	return h.saveSpec(s)
}

// AttachNIC records a switch name in the persisted spec. Effect on
// next StartVM (no NIC hotplug in vmd).
func (h *vmdHypervisor) AttachNIC(ctx context.Context, vmUUID string, nic drivers.NICHandle) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		return fmt.Errorf("AttachNIC: %w", err)
	}
	for _, n := range s.NICs {
		if n == nic.Device {
			return nil
		}
	}
	s.NICs = append(s.NICs, nic.Device)
	return h.saveSpec(s)
}

func (h *vmdHypervisor) DetachNIC(ctx context.Context, vmUUID, nicDevice string) error {
	s, err := h.loadSpec(vmUUID)
	if err != nil {
		return fmt.Errorf("DetachNIC: %w", err)
	}
	out := make([]string, 0, len(s.NICs))
	for _, n := range s.NICs {
		if n != nicDevice {
			out = append(out, n)
		}
	}
	s.NICs = out
	return h.saveSpec(s)
}
