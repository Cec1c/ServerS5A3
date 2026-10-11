package drop

// Operator rules, expressed per completed encounter, not per equipment roll.
const (
	EpicClearProbabilityBound  = 1000000
	TimeRiftEpicProbability    = 200000
	NormalAbyssEpicProbability = 130000
)

func EpicClearProbabilityHit(roll, probability int) bool {
	return roll >= 0 && roll < EpicClearProbabilityBound && probability >= 0 && probability <= EpicClearProbabilityBound && roll < probability
}
