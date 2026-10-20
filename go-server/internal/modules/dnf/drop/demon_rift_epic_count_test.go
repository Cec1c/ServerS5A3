package drop

import "testing"

func TestDemonRiftEpicCountExactDistributionAndBounds(t *testing.T) {
	var counts [5]int
	for roll := 0; roll < EpicClearProbabilityBound; roll++ {
		count := DemonRiftEpicCount(roll)
		if count < 1 || count > 4 {
			t.Fatalf("roll=%d count=%d", roll, count)
		}
		counts[count]++
	}
	if counts != [5]int{0, 500000, 300000, 150000, 50000} {
		t.Fatalf("count distribution=%v", counts)
	}
	if DemonRiftEpicCount(-1) != 0 || DemonRiftEpicCount(EpicClearProbabilityBound) != 0 {
		t.Fatal("invalid probability rolls accepted")
	}
}
