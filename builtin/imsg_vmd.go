// imsg_vmd.go — vmd-specific payload shapes encoded in the imsg
// envelope. The struct layouts match OpenBSD's vmd.h ; we serialise
// via encoding/binary and rely on Go's struct-tag-free fixed-size
// arrays (no padding ambiguities).

package builtin

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// Subset of vmop_create_params used at start-VM time. The full vmd.h
// struct carries ~30 fields covering accel hints, CD-ROMs, kernel
// arguments etc. — we serialise the subset we need and zero-pad the
// rest. The on-wire size MUST match what vmd's parser expects.
//
// Layout (host byte order = little-endian on amd64/arm64/riscv64) :
//
//   uint32_t  vmid               // 0 = new VM
//   uint32_t  vcp_id             // ignored on start
//   uint64_t  memranges_size     // we set memory inline below
//   uint64_t  pad0
//   char      name[64]
//   uint32_t  nnics
//   uint32_t  ndisks
//   uint32_t  ncdroms
//   uint32_t  nkernels
//   // memory ranges + interfaces + disks follow as variable-length
//   // arrays — we pin to one mem range, N nics, N disks, 0 cdroms.
//
// The actual vmd_create_params is more nuanced (memory ranges are a
// typed array of vmop_create_params_memrange { addr, size, type }).
// We serialise the simplest valid layout : 1 memrange covering
// [0, memBytes) of type vm_mem_type_ram (= 0).
const (
	vmMemTypeRAM = 0
)

// vmStartParams is the typed input to start(). Internal struct — the
// driver builds it from drivers.VMSpec.
type vmStartParams struct {
	Name       string
	MemMiB     int
	VCPUs      int
	BootKernel string   // empty = use vmd default ("/bsd")
	Disks      []string // absolute paths
	Nics       []string // switch names (e.g. "uplink") or empty for tap-on-bridge default
}

// startVM sends an IMSG_VMDOP_START_VM_REQUEST and parses the
// response. vmd assigns the VM ID and returns it in the response
// payload's `vmid` field (uint32 at offset 0).
func (c *imsgClient) startVM(ctx context.Context, p vmStartParams) (uint32, error) {
	if p.MemMiB <= 0 {
		return 0, errors.New("startVM: mem_mib must be > 0")
	}
	if p.VCPUs <= 0 {
		p.VCPUs = 1
	}
	payload := encodeStartVM(p)
	respCh := make(chan *imsgMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := c.request(imsgVmdopStartVMRequest, payload)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- r
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-errCh:
		return 0, err
	case resp := <-respCh:
		return parseStartVMResponse(resp)
	}
}

// encodeStartVM serialises vmStartParams into the vmd_create_params
// wire format. The implementation MUST stay in lock-step with vmd.h —
// when OpenBSD releases bump the struct layout, we re-vendor.
//
// Today we encode the SIMPLEST valid layout that vmd accepts :
//
//   uint32 vmid              = 0
//   uint32 ifnum             = len(nics)
//   uint64 nicmac[8]         = zeros (vmd will allocate)
//   uint64 memory_size_bytes = mem_mib * 1MiB
//   uint32 vcpus             = vcpus
//   char   name[64]          = padded VM name
//   uint32 ndisks            = len(disks)
//   for each disk : 1024-byte filename buffer + uint32 disktype
//   for each nic  : 64-byte switch name + uint32 nictype
//   char   kernel[1024]      = boot kernel path
//
// This is a simplified, weft-internal shape ; the real vmd accepts
// more fields. Operators wanting the full surface (CD-ROMs, custom
// memory ranges, NUMA hints) extend this struct as needed.
func encodeStartVM(p vmStartParams) []byte {
	var buf bytes.Buffer

	// Fixed-size header.
	binary.Write(&buf, binary.LittleEndian, uint32(0))                       // vmid
	binary.Write(&buf, binary.LittleEndian, uint32(len(p.Nics)))             // ifnum
	for i := 0; i < 8; i++ {
		binary.Write(&buf, binary.LittleEndian, uint64(0))                   // pre-allocated MACs
	}
	binary.Write(&buf, binary.LittleEndian, uint64(p.MemMiB)*1024*1024)      // memory_size_bytes
	binary.Write(&buf, binary.LittleEndian, uint32(p.VCPUs))                 // vcpus

	name := vmName64(p.Name)
	buf.Write(name[:])

	binary.Write(&buf, binary.LittleEndian, uint32(len(p.Disks)))            // ndisks

	// Disks : each entry is 1024-byte filename + uint32 type (0 = raw).
	for _, d := range p.Disks {
		writePaddedString(&buf, d, 1024)
		binary.Write(&buf, binary.LittleEndian, uint32(0))                   // disktype = raw
	}

	// NICs : 64-byte switch name + uint32 type (0 = tap-on-bridge).
	for _, n := range p.Nics {
		writePaddedString(&buf, n, 64)
		binary.Write(&buf, binary.LittleEndian, uint32(0))                   // nictype = bridge
	}

	// Boot kernel : 1024-byte path. Empty defaults to vmd's compiled-
	// in "/bsd" on the host.
	writePaddedString(&buf, p.BootKernel, 1024)
	return buf.Bytes()
}

// writePaddedString writes s padded with NULs to width bytes total.
// Strings longer than width-1 are truncated ; the trailing NUL is
// always present.
func writePaddedString(w *bytes.Buffer, s string, width int) {
	b := []byte(s)
	if len(b) > width-1 {
		b = b[:width-1]
	}
	w.Write(b)
	pad := make([]byte, width-len(b))
	w.Write(pad)
}

// parseStartVMResponse extracts the assigned VM ID from a
// IMSG_VMDOP_START_VM_RESPONSE message. The response shape is :
//
//   uint32 vmid           // the VM ID vmd assigned (or 0 on failure)
//   uint32 error_code     // 0 = success
//   char   error_string[256]
//
// vmd returns the same message type whether the start succeeded or
// failed ; we parse the error_code and surface it.
func parseStartVMResponse(msg *imsgMessage) (uint32, error) {
	if msg.Type != imsgVmdopStartVMResponse {
		return 0, fmt.Errorf("startVM: unexpected response type %d (want %d)", msg.Type, imsgVmdopStartVMResponse)
	}
	if len(msg.Payload) < 8 {
		return 0, fmt.Errorf("startVM: short response payload %d bytes", len(msg.Payload))
	}
	vmid := binary.LittleEndian.Uint32(msg.Payload[0:4])
	errCode := binary.LittleEndian.Uint32(msg.Payload[4:8])
	if errCode != 0 {
		// Try to extract the error string (NUL-terminated) if present.
		var errStr string
		if len(msg.Payload) >= 8+256 {
			b := msg.Payload[8 : 8+256]
			if i := bytes.IndexByte(b, 0); i >= 0 {
				errStr = string(b[:i])
			}
		}
		return 0, fmt.Errorf("vmd start error %d: %s", errCode, errStr)
	}
	return vmid, nil
}

// terminateVM sends an IMSG_VMDOP_TERMINATE_VM_REQUEST. force=true
// asks vmd to skip the ACPI-shutdown handshake.
//
// Payload : uint32 vmid + uint32 force + char name[64].
func (c *imsgClient) terminateVM(ctx context.Context, vmid uint32, name string, force bool) error {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, vmid)
	if force {
		binary.Write(&buf, binary.LittleEndian, uint32(1))
	} else {
		binary.Write(&buf, binary.LittleEndian, uint32(0))
	}
	n := vmName64(name)
	buf.Write(n[:])

	respCh := make(chan *imsgMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := c.request(imsgVmdopTerminateVMRequest, buf.Bytes())
		if err != nil {
			errCh <- err
			return
		}
		respCh <- r
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case resp := <-respCh:
		if resp.Type != imsgVmdopTerminateVMResponse {
			return fmt.Errorf("terminateVM: unexpected response type %d", resp.Type)
		}
		if len(resp.Payload) >= 4 {
			if errCode := binary.LittleEndian.Uint32(resp.Payload[0:4]); errCode != 0 {
				return fmt.Errorf("vmd terminate error %d", errCode)
			}
		}
		return nil
	}
}

// vmInfo carries the per-VM status fields vmd returns. The wire shape
// includes ~50 fields ; we extract the ones the driver actually uses
// (id, name, state, memory). Unknown fields are left in raw payload
// for a future extractor.
type vmInfo struct {
	ID     uint32
	Name   string
	State  string // "stopped" | "running" | "shutdown" | "unknown"
	MemMiB uint64
}

// vmStateName maps vmd's vm_state_t enum to operator-readable strings.
// Constants are stable across OpenBSD releases (vmd.h).
func vmStateName(state uint32) string {
	switch state {
	case 0:
		return "stopped"
	case 1:
		return "starting"
	case 2:
		return "running"
	case 3:
		return "shutdown"
	case 4:
		return "terminating"
	default:
		return "unknown"
	}
}

// getInfoVMs requests the full VM table from vmd. vmd streams one
// IMSG_VMDOP_GET_INFO_VM_RESPONSE per VM and closes with an empty
// IMSG_VMDOP_GET_INFO_VM_END_DATA.
//
// Returns the collected slice. Filtering by name is done in the
// caller — the wire protocol doesn't accept a server-side filter.
func (c *imsgClient) getInfoVMs(ctx context.Context) ([]vmInfo, error) {
	if err := c.send(imsgVmdopGetInfoVMRequest, 0, nil); err != nil {
		return nil, err
	}
	var out []vmInfo
	resultCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(resultCh)
		for {
			resp, err := c.recv()
			if err != nil {
				errCh <- err
				return
			}
			switch resp.Type {
			case imsgVmdopGetInfoVMEndData:
				return
			case imsgVmdopGetInfoVMResponse:
				if info, ok := decodeVMInfo(resp.Payload); ok {
					out = append(out, info)
				}
			default:
				// Unknown response type — skip.
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case <-resultCh:
		return out, nil
	}
}

// decodeVMInfo extracts the fields we use from a GET_INFO_VM_RESPONSE
// payload. Layout (from vmd.h `struct vmop_info_result`) :
//
//   struct vm_info_result {
//       uint32_t vir_id;
//       uint32_t vir_creator_pid;
//       uint64_t vir_memory_size;
//       uint32_t vir_ncpus;
//       uint32_t vir_running;       // 1 if cpu0 has booted past initial
//       char     vir_name[64];
//       uint32_t vir_state;         // vm_state_t
//       // ... more fields, ignored here
//   };
func decodeVMInfo(payload []byte) (vmInfo, bool) {
	// Minimum size we read = 4 + 4 + 8 + 4 + 4 + 64 + 4 = 92 bytes.
	if len(payload) < 92 {
		return vmInfo{}, false
	}
	info := vmInfo{
		ID:     binary.LittleEndian.Uint32(payload[0:4]),
		MemMiB: binary.LittleEndian.Uint64(payload[8:16]) / (1024 * 1024),
	}
	name := payload[24:88]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		info.Name = string(name[:i])
	} else {
		info.Name = string(name)
	}
	info.State = vmStateName(binary.LittleEndian.Uint32(payload[88:92]))
	return info, true
}

// findVM looks up a VM by name in vmd's table. Returns ErrVMNotFound
// when no row matches.
func (c *imsgClient) findVM(ctx context.Context, name string) (vmInfo, error) {
	vms, err := c.getInfoVMs(ctx)
	if err != nil {
		return vmInfo{}, err
	}
	for _, v := range vms {
		if v.Name == name {
			return v, nil
		}
	}
	return vmInfo{}, ErrVMNotFound
}

// ErrVMNotFound is returned by findVM when the named VM is absent
// from vmd's table — same semantic as `vmctl status` reporting "vm
// not found".
var ErrVMNotFound = errors.New("vmd: vm not found")

// createDiskRaw is a vmd-side disk provisioner. vmd doesn't expose a
// "create disk" imsg ; the operator-facing path is `vmctl create`,
// which under the hood writes a raw qcow2 header and pre-allocates
// the file. We do the same thing here in pure Go : create the file,
// truncate to the requested size. Raw format means the truncated
// file IS the disk image — vmd treats it as a flat block device.
func createDiskRaw(path string, sizeBytes int64) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := f.Truncate(sizeBytes); err != nil {
		// Roll back the partial file so a retry isn't blocked by the
		// O_EXCL flag.
		_ = os.Remove(path)
		return fmt.Errorf("truncate %s to %d: %w", path, sizeBytes, err)
	}
	return nil
}
