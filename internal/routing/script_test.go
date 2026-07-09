package routing

import (
	"strings"
	"testing"
)

func TestEmbeddedUpdateRoutesMarksLocalAndDockerTraffic(t *testing.T) {
	script := string(embeddedScript)
	for _, want := range []string{
		`DOCKER_IFACE="${DOCKER_IFACE:-br-docker}"`,
		`-t mangle -C OUTPUT -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK"`,
		`-t mangle -C PREROUTING -i "$DOCKER_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK"`,
		`-C FORWARD -i "$DOCKER_IFACE" -o "$VPN_IFACE" -j ACCEPT`,
		`cleanup_mangle_mark_rules`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("embedded update_routes.sh is missing %q", want)
		}
	}
}
