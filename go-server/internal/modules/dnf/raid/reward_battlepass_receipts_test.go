package raid

import (
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRewardBattlePassHistoricalReceiptProjection(t *testing.T) {
	marker := "raid_reward_v1_1_00112233445566778899aabbccddeeff_0_1"
	now := fateRewardTestTime()
	day := repo.ActivityDay(now)
	for _, tc := range []string{"valid", "already_counted", "partial", "zero_fingerprint", "missing_date", "before_period", "after_period", "negative", "over_cap", "invalid_card"} {
		t.Run(tc, func(t *testing.T) {
			ch := repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{marker: now.Unix(), marker + "_card": 1, marker + "_fate": 100, marker + "_hash0": 1, marker + "_hash1": 0, marker + "_hash2": 0, marker + "_hash3": 0}}
			switch tc {
			case "already_counted":
				ch.Stats[marker+"_bp_fate_v1"] = 1
			case "partial":
				delete(ch.Stats, marker+"_hash3")
			case "zero_fingerprint":
				ch.Stats[marker+"_hash0"] = 0
			case "missing_date":
				delete(ch.Stats, marker)
			case "before_period":
				ch.Stats[marker] = now.Add(-24 * time.Hour).Unix()
			case "after_period":
				ch.Stats[marker] = now.Add(24 * time.Hour).Unix()
			case "negative":
				ch.Stats[marker+"_fate"] = -1
			case "over_cap":
				ch.Stats[marker+"_fate"] = 2501
			case "invalid_card":
				ch.Stats[marker+"_card"] = 0
			}
			before := repo.CloneCharacter(ch)
			got := UncountedRewardFateByDay(ch, day, day)
			want := int64(0)
			if tc == "valid" {
				want = 100
			}
			if got[day] != want {
				t.Fatalf("receipt progress=%v want=%d", got, want)
			}
			if !reflect.DeepEqual(before, ch) {
				t.Fatal("projection changed durable receipt")
			}
		})
	}
}
