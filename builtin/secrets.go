package builtin

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	drivers "github.com/openweft/weft-drivers"
)

// ErrSecretsUnsupported is returned by CreateVM / StartVM when the VM
// spec carries a secret, inline or by reference. This driver has no
// vsock channel to deliver a secret to the guest safely, and it never
// falls back to inlining one (kernel cmdline, spec.json, disk): the
// request is refused. The text is constant so callers can match it.
var ErrSecretsUnsupported = errors.New("secrets are not supported by this driver: it has no vsock channel to deliver them safely")

// secretMarkers are case-insensitive substrings that name a guest
// secret field of weft-proto guestv1.PodSpec (Wireguard private_key /
// private_key_ref, CubeFSMount access_key / secret_key and their
// *_ref forms). "_ref" alone is not a marker: only a secret-named key
// is.
var secretMarkers = []string{
	"secret", "private_key", "privatekey", "private-key",
	"access_key", "accesskey", "access-key", "wireguard",
}

// RejectSecrets returns ErrSecretsUnsupported when spec carries a
// secret. drivers.VMSpec has no dedicated secret field, so the only
// free-form carriers are Cmdline and Name; both are inspected for a
// secret-named key or a raw WireGuard key (32 bytes, base64).
func RejectSecrets(spec drivers.VMSpec) error {
	for field, v := range map[string]string{"cmdline": spec.Cmdline, "name": spec.Name} {
		if carriesSecret(v) {
			return fmt.Errorf("%w (field %s)", ErrSecretsUnsupported, field)
		}
	}
	return nil
}

func carriesSecret(s string) bool {
	low := strings.ToLower(s)
	for _, m := range secretMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == ':' || r == ';' || r == '"' || r == '\''
	}) {
		// "key=<base64>": the value is whatever follows the first '='.
		if _, v, ok := strings.Cut(tok, "="); ok && isWireguardKey(v) {
			return true
		}
		if isWireguardKey(tok) {
			return true
		}
	}
	return false
}

func isWireguardKey(s string) bool {
	if len(s) != 44 {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}
