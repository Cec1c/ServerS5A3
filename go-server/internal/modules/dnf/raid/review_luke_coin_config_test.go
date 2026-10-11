package raid

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

type reviewLukeCoinConfigurationSource struct {
	dnfpvf.Source
	old, new string
}

func (s reviewLukeCoinConfigurationSource) ReadText(path string) (string, error) {
	v, e := s.Source.ReadText(path)
	if e == nil && path == "etc/raid/luke/luke.etc" {
		v = strings.Replace(v, s.old, s.new, 1)
	}
	return v, e
}
func TestReviewActualLukeCoinPhaseConfigurationAndMalformedLimits(t *testing.T) {
	ref := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if ref == "" {
		t.Skip("requires actual PVF")
	}
	a, e := platformpvf.Open(ref)
	if e != nil {
		t.Fatal(e)
	}
	c, e := LoadBattleConfiguration(a, "etc/raid/luke/luke.etc")
	if e != nil {
		t.Fatal(e)
	}
	if _, present := c.PhaseCoinLimits[0]; present || c.PhaseCoinLimits[1] != 20 || len(c.PhaseCoinLimits) != 1 {
		t.Fatal("actual Luke cap ownership not phase-two only", c.PhaseCoinLimits)
	}
	anton, e := LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if e != nil || len(anton.PhaseCoinLimits) != 0 {
		t.Fatal("Anton received Luke cap", e)
	}
	for _, tc := range []struct{ name, old, new string }{{"too-large", "[PHASE COIN LIMIT]\n20", "[PHASE COIN LIMIT]\n256"}, {"negative", "[PHASE COIN LIMIT]\n20", "[PHASE COIN LIMIT]\n-1"}, {"duplicate", "[PHASE COIN LIMIT]\n20", "[PHASE COIN LIMIT]\n20\n[PHASE COIN LIMIT]\n20"}, {"outside-phase", "[PHASE MAX]", "[PHASE COIN LIMIT]\n20\n[PHASE MAX]"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := LoadBattleConfiguration(reviewLukeCoinConfigurationSource{a, tc.old, tc.new}, "etc/raid/luke/luke.etc"); e == nil {
				t.Fatal("malformed phase cap accepted")
			}
		})
	}
}
