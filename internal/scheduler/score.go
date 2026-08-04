// Package scheduler implements a kube-scheduler HTTP extender: bin-pack vs
// spread node scoring, and a gang-scheduling capacity gate.
package scheduler

const (
	StrategyBinPack = "BinPack"
	StrategySpread  = "Spread"
)

// scoreNode returns an extender priority score in [0,10] for placing a job
// on a node with the given accelerator capacity/allocated. BinPack favors
// the fullest node (packs jobs onto fewer nodes); Spread favors the emptiest.
func scoreNode(strategy string, capacity, allocated int64) int64 {
	if capacity <= 0 {
		return 0
	}
	if allocated > capacity {
		allocated = capacity
	}
	ratio := allocated * 10 / capacity
	if strategy == StrategySpread {
		return 10 - ratio
	}
	return ratio
}

// gangFeasible reports whether enough free accelerator capacity exists across
// the candidate nodes to eventually seat every remaining member of a gang.
// ponytail: this only checks combined free capacity, not that it can fit as
// discrete per-node allocations, and doesn't atomically reserve it — a
// concurrent gang could still race for the same capacity. Real atomic
// reservation needs a Permit-stage scheduler plugin, not an HTTP extender.
// Upgrade path: move to a scheduler-plugins-based Coscheduling plugin if
// gang starvation/races show up under load.
func gangFeasible(gangSize, alreadyPlaced int, requestPerPod, totalFree int64) bool {
	remaining := int64(gangSize - alreadyPlaced)
	if remaining <= 0 {
		return true
	}
	return totalFree >= remaining*requestPerPod
}
