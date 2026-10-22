package dnfbridge

import "fmt"

// v153 registration 2032942 belongs to the class0 registry (25244A0,
// initialized by 2524320 -> 2031860). Receiver 2008770 reads exactly one u8,
// and 1A54AD0 stores it at raid manager+1A8. The phase check 2262500 compares
// that absolute count with 1A67360's signed resource limit, retaining -1.
func currentLukePhaseCoinBody(b *runtimeRaidBattle) ([]byte, error) {
	if b == nil || b.Mode != 2 || b.Single || b.Phase >= byte(len(b.PhaseCoins)) || b.PhaseCoins[b.Phase] > 255 {
		return nil, fmt.Errorf("Luke phase coin projection unavailable")
	}
	return []byte{byte(b.PhaseCoins[b.Phase])}, nil
}
