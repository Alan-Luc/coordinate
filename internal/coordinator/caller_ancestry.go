package coordinator

type processLink struct {
	Parent int
	UID    uint32
	Execed bool
}

// Pi-Bolt runs child processes through up to two forked helpers that never exec.
const maxCallerHelperHops = 2

func callerReachesParent(caller, parent int, lookup func(int) (processLink, error)) bool {
	if caller <= 0 || parent <= 0 {
		return false
	}
	current := parent
	for hop := 0; current != caller; hop++ {
		if hop == maxCallerHelperHops {
			return false
		}
		link, err := lookup(current)
		if err != nil || link.Execed {
			return false
		}
		above, err := lookup(link.Parent)
		if err != nil || above.UID != link.UID {
			return false
		}
		current = link.Parent
	}
	return true
}
