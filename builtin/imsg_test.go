// imsg_test.go — unit tests for the imsg encoding / decoding paths.
// No live vmd socket required ; the on-wire shape is exercised
// directly. The dial / send / recv path is exercised end-to-end via
// the Linux/OpenBSD integration suite when a real vmd is reachable.

package builtin

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestVMName64_TruncatesAndNULTerminates(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := vmName64(long)
	if out[vmdMaxName-1] != 0 {
		t.Errorf("trailing byte not NUL : %x", out[vmdMaxName-1])
	}
	// At most 63 bytes of payload + 1 NUL.
	payload := out[:vmdMaxName-1]
	for _, b := range payload {
		if b != 'x' {
			t.Fatalf("payload not preserved : got %x", b)
		}
	}
}

func TestVMName64_ShortNamePadded(t *testing.T) {
	out := vmName64("weft-abc")
	want := []byte("weft-abc")
	if !bytes.Equal(out[:len(want)], want) {
		t.Errorf("payload mismatch : got %q want %q", out[:len(want)], want)
	}
	// Trailing bytes must be zero.
	for i := len(want); i < vmdMaxName; i++ {
		if out[i] != 0 {
			t.Errorf("byte %d should be zero ; got %x", i, out[i])
		}
	}
}

func TestEncodeStartVM_LayoutMatchesVMDH(t *testing.T) {
	// Encode a known-good start request and check the byte layout
	// against the documented offsets. Catches struct-drift bugs
	// across OpenBSD releases.
	p := vmStartParams{
		Name:       "weft-test",
		MemMiB:     512,
		VCPUs:      2,
		BootKernel: "/bsd",
		Disks:      []string{"/disk0.img"},
		Nics:       []string{"uplink"},
	}
	enc := encodeStartVM(p)
	// Fixed header :
	//   vmid        @ 0   = 0
	//   ifnum       @ 4   = 1
	//   nicmac[8]   @ 8   = zeros (64 bytes)
	//   mem_bytes   @ 72  = 512 MiB
	//   vcpus       @ 80  = 2
	//   name[64]    @ 84
	//   ndisks      @ 148 = 1
	if got := binary.LittleEndian.Uint32(enc[0:4]); got != 0 {
		t.Errorf("vmid = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(enc[4:8]); got != 1 {
		t.Errorf("ifnum = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint64(enc[72:80]); got != 512*1024*1024 {
		t.Errorf("mem_bytes = %d, want %d", got, 512*1024*1024)
	}
	if got := binary.LittleEndian.Uint32(enc[80:84]); got != 2 {
		t.Errorf("vcpus = %d, want 2", got)
	}
	// Name region : "weft-test" + NUL pad.
	name := enc[84:148]
	if !bytes.HasPrefix(name, []byte("weft-test\x00")) {
		t.Errorf("name region malformed : %q", name)
	}
	if got := binary.LittleEndian.Uint32(enc[148:152]); got != 1 {
		t.Errorf("ndisks = %d, want 1", got)
	}
}

func TestParseStartVMResponse_Success(t *testing.T) {
	// vmid=42, error=0
	payload := make([]byte, 8+256)
	binary.LittleEndian.PutUint32(payload[0:4], 42)
	binary.LittleEndian.PutUint32(payload[4:8], 0)
	msg := &imsgMessage{Type: imsgVmdopStartVMResponse, Payload: payload}
	id, err := parseStartVMResponse(msg)
	if err != nil {
		t.Fatalf("parseStartVMResponse: %v", err)
	}
	if id != 42 {
		t.Errorf("vmid = %d, want 42", id)
	}
}

func TestParseStartVMResponse_ErrorCarriesString(t *testing.T) {
	payload := make([]byte, 8+256)
	binary.LittleEndian.PutUint32(payload[0:4], 0)
	binary.LittleEndian.PutUint32(payload[4:8], 5)
	copy(payload[8:], []byte("kernel not found\x00"))
	msg := &imsgMessage{Type: imsgVmdopStartVMResponse, Payload: payload}
	if _, err := parseStartVMResponse(msg); err == nil {
		t.Fatal("expected error from non-zero error_code")
	} else if !strings.Contains(err.Error(), "kernel not found") {
		t.Errorf("error_string not surfaced : %v", err)
	}
}

func TestParseStartVMResponse_WrongType(t *testing.T) {
	msg := &imsgMessage{Type: 99, Payload: make([]byte, 8)}
	if _, err := parseStartVMResponse(msg); err == nil {
		t.Error("wrong response type should error")
	}
}

func TestDecodeVMInfo_HappyPath(t *testing.T) {
	// Build a synthetic vm_info_result : id=7, mem=512 MiB,
	// name="vm-a", state=running (2).
	buf := make([]byte, 92)
	binary.LittleEndian.PutUint32(buf[0:4], 7)
	binary.LittleEndian.PutUint64(buf[8:16], 512*1024*1024)
	copy(buf[24:], []byte("vm-a\x00"))
	binary.LittleEndian.PutUint32(buf[88:92], 2) // running
	info, ok := decodeVMInfo(buf)
	if !ok {
		t.Fatal("decode failed")
	}
	if info.ID != 7 || info.MemMiB != 512 || info.Name != "vm-a" || info.State != "running" {
		t.Errorf("decoded = %+v", info)
	}
}

func TestDecodeVMInfo_ShortPayloadRejected(t *testing.T) {
	if _, ok := decodeVMInfo(make([]byte, 10)); ok {
		t.Error("short payload should NOT decode")
	}
}

func TestVMStateName_StableMapping(t *testing.T) {
	cases := []struct {
		state uint32
		want  string
	}{
		{0, "stopped"},
		{1, "starting"},
		{2, "running"},
		{3, "shutdown"},
		{4, "terminating"},
		{99, "unknown"},
	}
	for _, c := range cases {
		if got := vmStateName(c.state); got != c.want {
			t.Errorf("vmStateName(%d) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestWritePaddedString_RespectsWidthAndNULTerminates(t *testing.T) {
	var buf bytes.Buffer
	writePaddedString(&buf, "abcd", 8)
	got := buf.Bytes()
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8", len(got))
	}
	if string(got[:4]) != "abcd" || got[4] != 0 {
		t.Errorf("payload : %q", got)
	}
}

func TestWritePaddedString_TruncatesAndNULTerminates(t *testing.T) {
	var buf bytes.Buffer
	writePaddedString(&buf, "this-is-way-too-long", 8)
	got := buf.Bytes()
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8", len(got))
	}
	if got[7] != 0 {
		t.Errorf("trailing byte not NUL : %x", got[7])
	}
}
