package raid

import (
	"fmt"
	"testing"
)

func TestRaidDisconnectWaitConfigurationContract(t *testing.T) {
	base := "[PHASE MAX]\n1\n[START DELAY TIME]\n3\n[WEEKLY CLEAR COUNT]\n2\n[PHASE TIME OVER]\n2400\n[PHASE]\n[DUNGEON INFO]\n[INDEX]\n210\n[INIT STATE]\n`open`\n[/DUNGEON INFO]\n[/PHASE]\n"
	for _, row := range []struct {
		name, field string
		want        uint32
		reject      bool
	}{
		{"absent_preserves_fixture", "", 0, false}, {"actual300", "[DISCONN MEMBER WAIT TIME]\n300\n", 300, false}, {"zero", "[DISCONN MEMBER WAIT TIME]\n0\n", 0, true}, {"negative", "[DISCONN MEMBER WAIT TIME]\n-1\n", 0, true}, {"large", "[DISCONN MEMBER WAIT TIME]\n3601\n", 0, true}, {"invalid_token", "[DISCONN MEMBER WAIT TIME]\n`bad`\n", 0, true}, {"multiple", "[DISCONN MEMBER WAIT TIME]\n300 301\n", 0, true}, {"duplicate", "[DISCONN MEMBER WAIT TIME]\n300\n[DISCONN MEMBER WAIT TIME]\n300\n", 0, true}} {
		t.Run(row.name, func(t *testing.T) {
			got, err := LoadBattleConfiguration(admissionSource{"x": row.field + base}, "x")
			if (err != nil) != row.reject || !row.reject && got.DisconnectWaitSeconds != row.want {
				t.Fatal(fmt.Sprint(got.DisconnectWaitSeconds, err))
			}
		})
	}
}
