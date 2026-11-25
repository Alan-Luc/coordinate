package coordinator

type processLink struct {
	Parent  int
	UID     uint32
	Command string
}

// Pi-Bolt runs child processes through up to two same-executable helper processes.
const maxCallerHelperHops = 2

func callerReachesParent(caller, parent int, lookup func(int) (processLink, error)) bool {
	if caller <= 0 || parent <= 0 {
		return false
	}
	current := parent
	for hop := 0; ; hop++ {
		if current == caller {
			return true
		}
		if hop == maxCallerHelperHops {
			return false
		}
		link, err := lookup(current)
		if err != nil {
			return false
		}
		above, err := lookup(link.Parent)
		if err != nil || above.Command != link.Command || above.UID != link.UID {
			return false
		}
		current = link.Parent
	}
}
