package coordinator

import "testing"

func TestCallerReachesParentOnlyThroughForkedHelpers(t *testing.T) {
	tree := map[int]processLink{
		10: {Parent: 1, UID: 501, Execed: true},
		20: {Parent: 10, UID: 501, Execed: true},
		21: {Parent: 20, UID: 501},
		22: {Parent: 21, UID: 501},
		23: {Parent: 22, UID: 501},
		30: {Parent: 20, UID: 501, Execed: true},
		31: {Parent: 20, UID: 502},
		40: {Parent: 10, UID: 501, Execed: true},
		50: {Parent: 20, UID: 501, Execed: true},
		51: {Parent: 50, UID: 501},
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
		{"Pi-Bolt reaches through two forked helpers", 20, 22, true},
		{"three helper hops are too far", 20, 23, false},
		{"an exec'd child such as a shell or nested Pi is not its parent", 20, 30, false},
		{"a nested Pi cannot claim its parent Pi", 20, 50, false},
		{"a nested Pi-Bolt helper cannot reach the outer Pi", 20, 51, false},
		{"a helper owned by another user breaks the chain", 20, 31, false},
		{"unknown processes are refused", 20, 99, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerReachesParent(tc.caller, tc.parent, lookup); got != tc.want {
				t.Fatalf("callerReachesParent(%d, %d) = %v, want %v", tc.caller, tc.parent, got, tc.want)
			}
		})
	}
}
