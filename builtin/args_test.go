package builtin

import (
	"strings"
	"testing"
)

// argpair finds the value following the first occurrence of flag in args.
// Same helper shape as weft-driver-qemu's args_test.go -- copied so the
// args-builder tests read the same across the driver family.
func argpair(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

// argpairs returns every value following the given flag, in argv order,
// so tests can assert on multi-occurrence flags like -d.
func argpairs(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestBuildStartArgs_DirectKernel(t *testing.T) {
	args, err := buildStartArgs(startArgs{
		Name:       "vm-test",
		Kernel:     "/v/kernel",
		MemMiB:     1024,
		Disks:      []string{"/v/disk.img"},
		LocalIface: true,
	})
	if err != nil {
		t.Fatalf("buildStartArgs: %v", err)
	}

	if args[0] != "start" {
		t.Errorf("argv[0] = %q, want start", args[0])
	}
	if args[1] != "vm-test" {
		t.Errorf("VM name not in position 1: %v", args)
	}
	if k, _ := argpair(args, "-k"); k != "/v/kernel" {
		t.Errorf("-k = %q", k)
	}
	if m, _ := argpair(args, "-m"); m != "1024M" {
		t.Errorf("-m = %q, want 1024M", m)
	}
	if d, _ := argpair(args, "-d"); d != "/v/disk.img" {
		t.Errorf("-d = %q", d)
	}
	if !has(args, "-L") {
		t.Errorf("missing -L for LocalIface")
	}
}

func TestBuildStartArgs_MultipleDisks(t *testing.T) {
	args, _ := buildStartArgs(startArgs{
		Name:   "vm-multi",
		Kernel: "/k",
		MemMiB: 256,
		Disks:  []string{"/d/a.img", "/d/b.img"},
	})
	disks := argpairs(args, "-d")
	if len(disks) != 2 || disks[0] != "/d/a.img" || disks[1] != "/d/b.img" {
		t.Errorf("disks argv = %v", disks)
	}
}

func TestBuildStartArgs_DefaultsMemoryWhenZero(t *testing.T) {
	// MemMiB == 0 should fall back to the builder's 512 MiB default so
	// the caller doesn't have to remember to set both Options.DefaultMem
	// and the per-call MemMiB.
	args, _ := buildStartArgs(startArgs{Name: "vm", Kernel: "/k"})
	if m, _ := argpair(args, "-m"); m != "512M" {
		t.Errorf("default -m = %q, want 512M", m)
	}
}

func TestBuildStartArgs_NoLocalIfaceFlag(t *testing.T) {
	args, _ := buildStartArgs(startArgs{Name: "vm", Kernel: "/k"})
	if has(args, "-L") {
		t.Errorf("argv unexpectedly contains -L: %v", args)
	}
}

func TestBuildStartArgs_RequiresName(t *testing.T) {
	if _, err := buildStartArgs(startArgs{Kernel: "/k"}); err == nil {
		t.Error("expected error when name is empty")
	}
}

func TestBuildStartArgs_RequiresKernel(t *testing.T) {
	if _, err := buildStartArgs(startArgs{Name: "vm"}); err == nil {
		t.Error("expected error when kernel is empty")
	}
}

func TestBuildStopArgs_Graceful(t *testing.T) {
	args, err := buildStopArgs("vm-test", false)
	if err != nil {
		t.Fatalf("buildStopArgs: %v", err)
	}
	if strings.Join(args, " ") != "stop vm-test" {
		t.Errorf("graceful stop argv = %v", args)
	}
	if has(args, "-f") {
		t.Errorf("graceful stop should not contain -f: %v", args)
	}
}

func TestBuildStopArgs_Force(t *testing.T) {
	args, _ := buildStopArgs("vm-test", true)
	if !has(args, "-f") {
		t.Errorf("force stop missing -f: %v", args)
	}
	// Order matters : -f must come before the VM name so vmctl parses it
	// as a flag and not as a second VM name.
	if args[1] != "-f" || args[2] != "vm-test" {
		t.Errorf("force stop argv = %v, want [stop -f vm-test]", args)
	}
}

func TestBuildStopArgs_RequiresName(t *testing.T) {
	if _, err := buildStopArgs("", false); err == nil {
		t.Error("expected error when stop name is empty")
	}
}

func TestBuildCreateDiskArgs(t *testing.T) {
	args, err := buildCreateDiskArgs("/v/disk.img", 8)
	if err != nil {
		t.Fatalf("buildCreateDiskArgs: %v", err)
	}
	if strings.Join(args, " ") != "create -s 8G /v/disk.img" {
		t.Errorf("argv = %v", args)
	}
}

func TestBuildCreateDiskArgs_RequiresPath(t *testing.T) {
	if _, err := buildCreateDiskArgs("", 1); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestBuildCreateDiskArgs_RequiresSize(t *testing.T) {
	if _, err := buildCreateDiskArgs("/p", 0); err == nil {
		t.Error("expected error for zero size")
	}
	if _, err := buildCreateDiskArgs("/p", -1); err == nil {
		t.Error("expected error for negative size")
	}
}
