package applications

import (
	"errors"
	"testing"
)

// Every pair, as ADR 0008 asks: forward and to rejected/archived are free, same status is
// fine, anything else needs force.
func TestTransitionEveryPair(t *testing.T) {
	statuses := []string{Discovered, "saved", "applied", "interview", "offer", "rejected", "archived"}
	free := map[[2]string]bool{}
	for _, from := range statuses {
		for _, to := range statuses[1:] {
			ok := from == to || to == "rejected" || to == "archived" || from == Discovered ||
				(from != "rejected" && from != "archived" && forward[to] > forward[from])
			free[[2]string{from, to}] = ok
		}
	}
	// Spot-check the rule table against the ADR in plain words.
	for pair, want := range map[[2]string]bool{
		{"saved", "interview"}: true, {"offer", "applied"}: false, {"rejected", "saved"}: false,
		{"interview", "rejected"}: true, {Discovered, "offer"}: true, {"archived", "archived"}: true,
	} {
		if free[pair] != want {
			t.Fatalf("test table disagrees with the ADR on %v", pair)
		}
	}
	for pair, ok := range free {
		err := Transition(pair[0], pair[1], false)
		if (err == nil) != ok {
			t.Errorf("%s -> %s without force: err = %v", pair[0], pair[1], err)
		}
		if !ok && !errors.Is(err, ErrBackwards) {
			t.Errorf("%s -> %s: want ErrBackwards, got %v", pair[0], pair[1], err)
		}
		if err := Transition(pair[0], pair[1], true); err != nil {
			t.Errorf("%s -> %s with force: %v", pair[0], pair[1], err)
		}
	}
}

func TestTransitionRejectsUnknownStatuses(t *testing.T) {
	for _, pair := range [][2]string{{"saved", Discovered}, {"saved", "hired"}, {"hired", "saved"}} {
		if err := Transition(pair[0], pair[1], true); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: got %v", pair, err)
		}
	}
}
