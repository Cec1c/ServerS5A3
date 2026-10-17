package inventory

// A quote failure happens before RNG or asset writes and can safely return the
// existing invalid-material rejection. Preserve the cause for server diagnostics.
type upgradeCostResolutionError struct{ cause error }

func (e *upgradeCostResolutionError) Error() string {
	return "resolve upgrade cost: " + e.cause.Error()
}
func (e *upgradeCostResolutionError) Unwrap() error { return e.cause }
