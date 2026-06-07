// imsg.go — pure-Go client for the OpenBSD imsg(3) protocol against
// the vmd(8) control socket at /var/run/vmd.sock.
//
// imsg is OpenBSD's structured-IPC framing : a 16-byte header
// (type+length+flags+peerid+pid) followed by a payload. File
// descriptors travel as SCM_RIGHTS ancillary data on the Unix socket,
// which is how vmd hands back the guest's PTY / console fd.
//
// This implementation is hand-rolled — no cgo, no vmctl exec, no
// third-party deps. The wire format is documented in imsg(3) ; the
// vmd-specific message types come from OpenBSD's
// usr.sbin/vmd/vmd.h (IMSG_VMDOP_* constants).
//
// Why direct imsg instead of vmctl(8) :
//  - No process spawn per call (vmctl takes ~30ms to fork+exec).
//  - Binary framing : no fragile output parsing.
//  - vmd reports task status synchronously via the response message,
//    so the driver doesn't need to poll.
//  - The driver can subscribe to vmd's event stream once it implements
//    the watch path (future).
//
// Build constraint : the syscall.Cmsghdr + Unix socket SCM_RIGHTS
// path is POSIX-portable Go ; the imsg framing is endian-defined
// (host byte order on the host running vmd). Since vmd only runs on
// OpenBSD and OpenBSD only runs on little-endian arches in practice
// (amd64, arm64, riscv64, loongarch), we hard-code little-endian.
// The fallback for big-endian OpenBSD/sparc64 would be one switch
// statement and is left out.

package builtin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

// imsg wire format — matches /usr/include/imsg.h on OpenBSD.
//
//   struct imsg_hdr {
//       uint32_t type;
//       uint16_t len;       // header + payload, max 16384
//       uint16_t flags;
//       uint32_t peerid;
//       uint32_t pid;
//   };
//
// All fields are host byte order. We commit to little-endian — see
// the package header for the rationale.

const (
	imsgHdrSize = 16
	imsgMaxLen  = 16384 // hard cap from imsg.h IMSG_HEADER_SIZE+MAX_IMSGSIZE
)

// vmd message types we use. Sourced from openbsd/src/usr.sbin/vmd/vmd.h
// (IMSG_VMDOP_*). The numeric values are stable across releases.
const (
	imsgVmdopStartVMRequest     = 1
	imsgVmdopStartVMResponse    = 2
	imsgVmdopTerminateVMRequest = 5
	imsgVmdopTerminateVMResponse = 6
	imsgVmdopGetInfoVMRequest   = 8
	imsgVmdopGetInfoVMResponse  = 9
	imsgVmdopGetInfoVMEndData   = 10
)

// vmdMaxName mirrors VMM_MAX_NAME_LEN — the per-VM name has to fit
// in 64 bytes including the NUL terminator. We truncate at 63.
const vmdMaxName = 64

// imsgMessage is one received message — header fields exploded out
// + the trailing payload bytes. The fd (when non-nil) comes from
// SCM_RIGHTS ancillary data.
type imsgMessage struct {
	Type    uint32
	PeerID  uint32
	PID     uint32
	Flags   uint16
	Payload []byte
	FD      *os.File
}

// imsgClient is the typed wrapper around the Unix-socket connection.
// Concurrent use is serialised internally — vmd's imsg protocol is
// request/response per connection.
type imsgClient struct {
	mu   sync.Mutex
	conn *net.UnixConn
	pid  uint32
}

// dial opens a connection to the vmd control socket. The default
// path (/var/run/vmd.sock) requires root or membership in the _vmd
// group ; on a typical OpenBSD weft host the weft-agent service runs
// with the right group attached.
func dialImsg(path string) (*imsgClient, error) {
	if path == "" {
		path = "/var/run/vmd.sock"
	}
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", path, err)
	}
	return &imsgClient{conn: conn, pid: uint32(os.Getpid())}, nil
}

// close terminates the socket. Idempotent.
func (c *imsgClient) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// send writes a single imsg message. peerid + pid are stamped from
// the client state ; flags are 0 unless the caller needs them.
func (c *imsgClient) send(msgType uint32, peerID uint32, payload []byte) error {
	if len(payload)+imsgHdrSize > imsgMaxLen {
		return fmt.Errorf("imsg send: payload too large (%d bytes ; max %d)", len(payload), imsgMaxLen-imsgHdrSize)
	}
	buf := make([]byte, imsgHdrSize+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], msgType)
	binary.LittleEndian.PutUint16(buf[4:6], uint16(imsgHdrSize+len(payload)))
	binary.LittleEndian.PutUint16(buf[6:8], 0) // flags
	binary.LittleEndian.PutUint32(buf[8:12], peerID)
	binary.LittleEndian.PutUint32(buf[12:16], c.pid)
	copy(buf[imsgHdrSize:], payload)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("imsg send: connection closed")
	}
	if _, err := c.conn.Write(buf); err != nil {
		return fmt.Errorf("imsg write: %w", err)
	}
	return nil
}

// recv reads one imsg message. It blocks until the full header +
// payload is available. SCM_RIGHTS ancillary data (when vmd attaches
// a file descriptor, e.g. for the console PTY) is exposed via
// imsgMessage.FD.
func (c *imsgClient) recv() (*imsgMessage, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("imsg recv: connection closed")
	}

	hdr := make([]byte, imsgHdrSize)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, fmt.Errorf("imsg read header: %w", err)
	}
	total := binary.LittleEndian.Uint16(hdr[4:6])
	if int(total) < imsgHdrSize {
		return nil, fmt.Errorf("imsg recv: malformed length %d", total)
	}
	payloadLen := int(total) - imsgHdrSize
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, fmt.Errorf("imsg read payload: %w", err)
		}
	}
	msg := &imsgMessage{
		Type:    binary.LittleEndian.Uint32(hdr[0:4]),
		Flags:   binary.LittleEndian.Uint16(hdr[6:8]),
		PeerID:  binary.LittleEndian.Uint32(hdr[8:12]),
		PID:     binary.LittleEndian.Uint32(hdr[12:16]),
		Payload: payload,
	}
	// vmd attaches file descriptors via SCM_RIGHTS on the AUX channel
	// — net.UnixConn exposes them via ReadMsgUnix. For the v0.1 path
	// (start/stop/get-info) no FDs travel ; we leave the ancillary
	// channel read for the future console-PTY path.
	return msg, nil
}

// request is the synchronous round-trip helper : send one message,
// read one response. Used by every vmd lifecycle call.
func (c *imsgClient) request(msgType uint32, payload []byte) (*imsgMessage, error) {
	if err := c.send(msgType, 0, payload); err != nil {
		return nil, err
	}
	resp, err := c.recv()
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// vmName64 pads / truncates a name to the 64-byte vmd_vm_params shape.
func vmName64(name string) [vmdMaxName]byte {
	var out [vmdMaxName]byte
	b := []byte(name)
	if len(b) > vmdMaxName-1 {
		b = b[:vmdMaxName-1]
	}
	copy(out[:], b)
	// Trailing bytes already zero.
	return out
}
