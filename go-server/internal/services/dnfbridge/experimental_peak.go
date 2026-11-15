package dnfbridge

import "os"

// The selected peak optimizations are opt-in for ordinary server builds.
func experimentalPeakEnabled() bool { return os.Getenv("DNFBRIDGE_EXPERIMENTAL_PEAK") == "on" }
