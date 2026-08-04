package scheduler

import "testing"

func TestScoreNodeBinPackFavorsFullerNode(t *testing.T) {
	emptyScore := scoreNode(StrategyBinPack, 10, 0)
	fullerScore := scoreNode(StrategyBinPack, 10, 8)
	if fullerScore <= emptyScore {
		t.Errorf("BinPack should score a fuller node higher: empty=%d fuller=%d", emptyScore, fullerScore)
	}
}

func TestScoreNodeSpreadFavorsEmptierNode(t *testing.T) {
	emptyScore := scoreNode(StrategySpread, 10, 0)
	fullerScore := scoreNode(StrategySpread, 10, 8)
	if emptyScore <= fullerScore {
		t.Errorf("Spread should score an emptier node higher: empty=%d fuller=%d", emptyScore, fullerScore)
	}
}

func TestScoreNodeZeroCapacity(t *testing.T) {
	if got := scoreNode(StrategyBinPack, 0, 0); got != 0 {
		t.Errorf("expected 0 score for zero-capacity node, got %d", got)
	}
}

func TestScoreNodeClampsOverAllocated(t *testing.T) {
	// Shouldn't happen in practice, but a node reported as over-allocated
	// must still produce a score in range rather than going negative/huge.
	got := scoreNode(StrategyBinPack, 10, 15)
	if got < 0 || got > 10 {
		t.Errorf("expected score in [0,10] even when allocated > capacity, got %d", got)
	}
}

func TestGangFeasibleEnoughCapacity(t *testing.T) {
	// 3-pod gang, 1 already placed, 1 accelerator each, 5 free total.
	if !gangFeasible(3, 1, 1, 5) {
		t.Error("expected feasible: 2 remaining pods need 2, have 5 free")
	}
}

func TestGangFeasibleNotEnoughCapacity(t *testing.T) {
	// 3-pod gang, 0 placed, 2 accelerators each (needs 6), only 4 free.
	if gangFeasible(3, 0, 2, 4) {
		t.Error("expected infeasible: 3 pods need 6, only 4 free")
	}
}

func TestGangFeasibleAllAlreadyPlaced(t *testing.T) {
	if !gangFeasible(3, 3, 1, 0) {
		t.Error("expected feasible once all gang members are already placed")
	}
}
