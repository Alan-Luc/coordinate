package coordinator

import "testing"

func TestCallerReachesParentThroughSameExecutableHelpers(t *testing.T) {
	tree := map[int]processLink{
		10: {Parent: 1, UID: 501, Command: "zsh"},
		20: {Parent: 10, UID: 501, Command: "pi-bolt"},
		21: {Parent: 20, UID: 501, Command: "pi-bolt"},
		22: {Parent: 21, UID: 501, Command: "pi-bolt"},
		23: {Parent: 22, UID: 501, Command: "pi-bolt"},
		30: {Parent: 20, UID: 501, Command: "sh"},
		31: {Parent: 21, UID: 502, Command: "pi-bolt"},
		40: {Parent: 10, UID: 501, Command: "node"},
	}
	lookup := func(pid int) (processLink, error) {
		link, ok := tree[pid]
		if !ok {
			return processLink{}, ErrProcessGone
		}
		return link, nil
	}
	for _, tc := range []struct {
		name           string
		caller, parent int
		want           bool
	}{
		{"node Pi is the direct parent", 40, 40, true},
		{"Pi-Bolt reaches through two helpers", 20, 22, true},
		{"three helper hops are too far", 20, 23, false},
		{"a shell child of Pi is not Pi", 20, 30, false},
		{"a helper owned by another user breaks the chain", 21, 31, false},
		{"an unrelated ancestor is refused", 10, 22, false},
		{"unknown processes are refused", 20, 99, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerReachesParent(tc.caller, tc.parent, lookup); got != tc.want {
				t.Fatalf("callerReachesParent(%d, %d) = %v, want %v", tc.caller, tc.parent, got, tc.want)
			}
		})
	}
}
