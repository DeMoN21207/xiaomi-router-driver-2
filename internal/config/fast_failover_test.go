package config

import "testing"

func TestNormalizePreserves15SecondFailover(t *testing.T) {
	state := DefaultState()
	state.Automation.FailoverFailureSeconds = 15
	if got := normalize(state).Automation.FailoverFailureSeconds; got != 15 {
		t.Fatalf("15 second threshold changed to %d", got)
	}
}
