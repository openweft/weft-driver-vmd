package builtin

import (
	"errors"
	"strings"
	"testing"

	drivers "github.com/openweft/weft-drivers"
)

const wgKey = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="

func TestRejectSecrets(t *testing.T) {
	cases := []struct {
		name    string
		spec    drivers.VMSpec
		refused bool
	}{
		{"clean", drivers.VMSpec{UUID: "u", Name: "web", Cmdline: "console=ttyS0 root=/dev/vda"}, false},
		{"empty", drivers.VMSpec{}, false},
		{"inline wireguard key", drivers.VMSpec{Cmdline: "console=ttyS0 wg=" + wgKey}, true},
		{"bare wireguard key", drivers.VMSpec{Cmdline: wgKey}, true},
		{"named private key", drivers.VMSpec{Cmdline: "PRIVATE_KEY=x"}, true},
		{"cubefs secret key", drivers.VMSpec{Cmdline: "cubefs.secret_key=abc"}, true},
		{"cubefs access key ref", drivers.VMSpec{Cmdline: "access_key_ref=vault/x"}, true},
		{"private key ref", drivers.VMSpec{Cmdline: "private_key_ref=vault/x"}, true},
		{"secret in name", drivers.VMSpec{Name: "my-secret"}, true},
		{"44 chars not base64", drivers.VMSpec{Cmdline: strings.Repeat("!", 44)}, false},
		{"44 chars wrong length", drivers.VMSpec{Cmdline: strings.Repeat("A", 40) + "AAAA"}, false},
	}
	for _, c := range cases {
		err := RejectSecrets(c.spec)
		if c.refused != (err != nil) {
			t.Errorf("%s: refused=%v err=%v", c.name, c.refused, err)
		}
		if c.refused && !errors.Is(err, ErrSecretsUnsupported) {
			t.Errorf("%s: error %v is not ErrSecretsUnsupported", c.name, err)
		}
	}
}

func TestCreateVMRefusesSecrets(t *testing.T) {
	h := &vmdHypervisor{}
	err := h.CreateVM(t.Context(), drivers.VMSpec{UUID: "u", Cmdline: "secret_key=abc"})
	if !errors.Is(err, ErrSecretsUnsupported) {
		t.Fatalf("CreateVM err = %v, want ErrSecretsUnsupported", err)
	}
}
