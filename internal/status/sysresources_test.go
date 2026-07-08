package status

import "testing"

func TestParseSystemUptime(t *testing.T) {
	uptime := parseSystemUptime("90061.23 120.00\n")

	if uptime.Seconds != 90061 {
		t.Fatalf("Seconds = %d, want 90061", uptime.Seconds)
	}
	if uptime.Formatted != "1д 1ч 1м" {
		t.Fatalf("Formatted = %q, want 1д 1ч 1м", uptime.Formatted)
	}
}
