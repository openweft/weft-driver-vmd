package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	drivers "github.com/openweft/weft-drivers"
)

// vmd_test.go covers the lifecycle stubs themselves : helpers that touch
// the VM directory (CreateVM, AttachDisk) and the shell-out methods using
// /usr/bin/true as a vmctl stand-in. The pure argv builder is tested in
// args_test.go ; this file is about the OS-level glue.

func TestNewHypervisor_FillsDefaults(t *testing.T) {
	h := NewHypervisor(Options{})
	if h.opts.VmctlBinary != "vmctl" {
		t.Errorf("VmctlBinary = %q, want vmctl", h.opts.VmctlBinary)
	}
	if h.opts.DefaultMemMiB != 512 {
		t.Errorf("DefaultMemMiB = %d, want 512", h.opts.DefaultMemMiB)
	}
	if h.opts.DefaultCPUs != 1 {
		t.Errorf("DefaultCPUs = %d, want 1", h.opts.DefaultCPUs)
	}
}

func TestHostInfo_ReportsVmd(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h1", Hostname: "obsd1"})
	hi, _ := h.HostInfo(context.Background())
	if hi.Hypervisor != "vmd" {
		t.Errorf("Hypervisor = %q, want vmd", hi.Hypervisor)
	}
	if hi.UUID != "h1" || hi.Hostname != "obsd1" {
		t.Errorf("UUID/Hostname = %q/%q", hi.UUID, hi.Hostname)
	}
	// Architecture follows the host (not the guest -- vmd only runs on
	// amd64/arm64 OpenBSD anyway). hostArchForGOARCH normalises to
	// openweft labels.
	if hi.Architecture != runtime.GOARCH {
		t.Errorf("Architecture = %q, want %q (host GOARCH)", hi.Architecture, runtime.GOARCH)
	}
}

func TestCreateVM_MakesDirAndMACAndName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vm1")
	h := NewHypervisor(Options{})
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: dir, Name: "named-vm"}); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("vmDir not created: %v", err)
	}
	mac, err := os.ReadFile(filepath.Join(dir, "mac.txt"))
	if err != nil {
		t.Fatalf("mac.txt: %v", err)
	}
	if len(mac) != len("02:00:00:00:00:00") {
		t.Errorf("mac.txt malformed: %q", mac)
	}
	name, err := os.ReadFile(filepath.Join(dir, "name"))
	if err != nil {
		t.Fatalf("name file: %v", err)
	}
	if string(name) != "named-vm" {
		t.Errorf("name file = %q, want named-vm", name)
	}

	// Idempotent : second call keeps the same MAC + name.
	_ = h.CreateVM(context.Background(), drivers.VMSpec{UUID: dir, Name: "other"})
	mac2, _ := os.ReadFile(filepath.Join(dir, "mac.txt"))
	if string(mac) != string(mac2) {
		t.Error("CreateVM regenerated the MAC (not idempotent)")
	}
	name2, _ := os.ReadFile(filepath.Join(dir, "name"))
	if string(name2) != "named-vm" {
		t.Errorf("CreateVM overwrote the name (not idempotent): %q", name2)
	}
}

func TestCreateVM_NamesFromDirWhenSpecEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fallback-name")
	h := NewHypervisor(Options{})
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: dir}); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	name, _ := os.ReadFile(filepath.Join(dir, "name"))
	if string(name) != "fallback-name" {
		t.Errorf("name file fallback = %q, want fallback-name", name)
	}
}

func TestCreateVM_EmptyUUIDErrors(t *testing.T) {
	h := NewHypervisor(Options{})
	if err := h.CreateVM(context.Background(), drivers.VMSpec{}); err == nil {
		t.Error("expected error for empty UUID")
	}
}

func TestStopVM_MissingDirIsNoOp(t *testing.T) {
	h := NewHypervisor(Options{})
	if err := h.StopVM(context.Background(), filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("StopVM on absent dir = %v, want nil", err)
	}
}

func TestDeleteVM_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no 'true' binary to stand in for vmctl")
	}
	h := NewHypervisor(Options{VmctlBinary: stub})
	if err := h.DeleteVM(context.Background(), dir); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("vmDir still present after DeleteVM")
	}
	if err := h.DeleteVM(context.Background(), dir); err != nil {
		t.Errorf("DeleteVM (missing) = %v, want nil", err)
	}
}

// TestAttachDisk_ShellsOutToVmctl confirms the file-missing branch execs
// the configured vmctl binary. We point VmctlBinary at /usr/bin/true so
// the call succeeds without actually creating anything ; the assertion is
// that the second call (with the file now present) is the idempotent
// no-op branch.
func TestAttachDisk_ShellsOutToVmctl(t *testing.T) {
	stub, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no 'true' binary to stand in for vmctl")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.img")
	h := NewHypervisor(Options{VmctlBinary: stub})
	if err := h.AttachDisk(context.Background(), dir, drivers.DiskSpec{BackingPath: path, SizeGiB: 1}); err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}
	// /usr/bin/true doesn't actually create the file -- we touch it here
	// to exercise the second call's "already exists" idempotent path.
	if err := os.WriteFile(path, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.AttachDisk(context.Background(), dir, drivers.DiskSpec{BackingPath: path, SizeGiB: 1}); err != nil {
		t.Errorf("AttachDisk (existing) = %v, want nil (idempotent no-op)", err)
	}
}

func TestAttachDisk_MissingZeroSizeErrors(t *testing.T) {
	h := NewHypervisor(Options{})
	err := h.AttachDisk(context.Background(), t.TempDir(),
		drivers.DiskSpec{BackingPath: filepath.Join(t.TempDir(), "x.img"), SizeGiB: 0})
	if err == nil {
		t.Error("expected error for missing file with SizeGiB==0")
	}
}

func TestDetachDisk_IsNoOp(t *testing.T) {
	h := NewHypervisor(Options{})
	if err := h.DetachDisk(context.Background(), "any", "any"); err != nil {
		t.Errorf("DetachDisk = %v, want nil (vmd has no hot-unplug)", err)
	}
}

func TestAttachDetachNIC_Unsupported(t *testing.T) {
	h := NewHypervisor(Options{})
	if err := h.AttachNIC(context.Background(), "vm", drivers.NICHandle{}); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("AttachNIC = %v, want ErrUnsupported", err)
	}
	if err := h.DetachNIC(context.Background(), "vm", "tap0"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("DetachNIC = %v, want ErrUnsupported", err)
	}
}

func TestStartVM_NoNameFileErrors(t *testing.T) {
	// CreateVM is what writes the name file ; calling StartVM on a bare
	// directory must error so a stale registry pointer doesn't silently
	// boot the wrong VM.
	h := NewHypervisor(Options{})
	if err := h.StartVM(context.Background(), t.TempDir()); err == nil {
		t.Error("expected error when name file is missing")
	}
}

func TestStartVM_NoKernelErrors(t *testing.T) {
	dir := t.TempDir()
	// Pre-write the name file so we reach the kernel-stat check.
	if err := os.WriteFile(filepath.Join(dir, "name"), []byte("vm"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHypervisor(Options{})
	if err := h.StartVM(context.Background(), dir); err == nil {
		t.Error("expected error when kernel is missing")
	}
}

// TestStartVM_ExecsVmctl exercises the happy path with /usr/bin/true
// standing in for vmctl. Confirms the helper assembles a valid argv and
// runVmctl propagates a zero exit.
func TestStartVM_ExecsVmctl(t *testing.T) {
	stub, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no 'true' binary to stand in for vmctl")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "name"), []byte("vm-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kernel"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHypervisor(Options{VmctlBinary: stub})
	if err := h.StartVM(context.Background(), dir); err != nil {
		t.Errorf("StartVM = %v, want nil", err)
	}
}

// TestStopVM_ExecsAndSwallowsError confirms that even when vmctl fails
// (here false(1) returns 1), StopVM treats it as success -- that's the
// idempotence contract for "VM already gone".
func TestStopVM_ExecsAndSwallowsError(t *testing.T) {
	stub, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no 'false' binary to stand in for vmctl")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "name"), []byte("vm-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHypervisor(Options{VmctlBinary: stub})
	if err := h.StopVM(context.Background(), dir); err != nil {
		t.Errorf("StopVM = %v, want nil (idempotence swallows vmctl errors)", err)
	}
}

func TestNetwork_AllUnsupported(t *testing.T) {
	n := NewNetwork(Options{})
	ctx := context.Background()
	if err := n.EnsureNetwork(ctx, drivers.NetworkSpec{}); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("EnsureNetwork = %v", err)
	}
	if err := n.DestroyNetwork(ctx, "x"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("DestroyNetwork = %v", err)
	}
	if _, err := n.AttachPort(ctx, drivers.PortSpec{}); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("AttachPort = %v", err)
	}
	if err := n.DetachPort(ctx, "p"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("DetachPort = %v", err)
	}
	if err := n.RotateMeshPeer(ctx, drivers.PortSpec{}); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("RotateMeshPeer = %v", err)
	}
}

func TestVolume_FileBackend(t *testing.T) {
	v := NewVolume(VolumeOptions{StateDir: "/tmp/x"})
	if v.Name() != "file" {
		t.Errorf("Name = %q, want file", v.Name())
	}
	if !v.Local() {
		t.Errorf("Local() = false, want true (file backend is host-bound)")
	}
}

func TestImage_InCacheReportsFalse(t *testing.T) {
	i := NewImage(ImageOptions{CacheDir: "/tmp/x"})
	ok, err := i.InCache(context.Background(), "any:tag")
	if err != nil {
		t.Errorf("InCache err = %v", err)
	}
	if ok {
		t.Errorf("InCache = true, want false (scaffold cache is empty)")
	}
}

func TestBundleNew_SharesHostInfo(t *testing.T) {
	b := New(BundleOptions{Options: Options{HostUUID: "host-A"}, StateDir: "/tmp/state"})
	ctx := context.Background()
	hi1, _ := b.Hypervisor.HostInfo(ctx)
	hi2, _ := b.Network.HostInfo(ctx)
	hi3, _ := b.Volume.HostInfo(ctx)
	hi4, _ := b.Image.HostInfo(ctx)
	for _, hi := range []drivers.HostInfo{hi1, hi2, hi3, hi4} {
		if hi.UUID != "host-A" || hi.Hypervisor != "vmd" {
			t.Errorf("bundle host info diverged: %+v", hi)
		}
	}
}
