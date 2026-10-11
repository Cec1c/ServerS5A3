package drop

import "testing"

func TestCompletedEncounterEpicProbability(t *testing.T) {
	for _, probability := range []int{0, NormalAbyssEpicProbability, TimeRiftEpicProbability, EpicClearProbabilityBound} {
		hits := 0
		for roll := 0; roll < EpicClearProbabilityBound; roll++ {
			if EpicClearProbabilityHit(roll, probability) {
				hits++
			}
		}
		if hits != probability {
			t.Fatalf("probability %d accepted %d outcomes", probability, hits)
		}
		if EpicClearProbabilityHit(-1, probability) || EpicClearProbabilityHit(EpicClearProbabilityBound, probability) {
			t.Fatal("invalid random outcome accepted")
		}
	}
}
