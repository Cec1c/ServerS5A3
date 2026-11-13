package raid

import (
	"encoding/hex"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// UncountedRewardFateByDay projects old committed ledger receipts which predate
// the battle-pass counter fix. It never credits currency, rewrites receipts or
// guesses an acquisition date from the current wallet balance/weekly window.
func UncountedRewardFateByDay(character repo.CharacterRecord, from, through string) map[string]int64 {
	result := map[string]int64{}
	for key, amount := range character.Stats {
		if !strings.HasPrefix(key, "raid_reward_v1_") || !strings.HasSuffix(key, "_fate") || amount <= 0 || amount > int64(raidcurrency.WeeklyLimit) {
			continue
		}
		marker := strings.TrimSuffix(key, "_fate")
		parts := strings.Split(marker, "_")
		if len(parts) != 7 || (parts[3] != "1" && parts[3] != "2" && parts[3] != "3") || (parts[5] != "0" && parts[5] != "1") || (parts[6] != "1" && parts[6] != "2" && parts[6] != "3") || parts[3] == "3" && parts[5] != "0" {
			continue
		}
		id, err := hex.DecodeString(parts[4])
		if err != nil || len(id) != 16 {
			continue
		}
		if _, counted := character.Stats[marker+"_bp_fate_v1"]; counted {
			continue
		}
		stamp, present := character.Stats[marker]
		card := character.Stats[marker+"_card"]
		if !present || stamp <= 0 || card < 1 || card > 20 || parts[3] == "3" && card > 8 {
			continue
		}
		complete, fingerprint := true, int64(0)
		for _, suffix := range []string{"_hash0", "_hash1", "_hash2", "_hash3"} {
			value, found := character.Stats[marker+suffix]
			complete = complete && found
			fingerprint |= value
		}
		if !complete || fingerprint == 0 {
			continue
		}
		day := repo.ActivityDay(time.Unix(stamp, 0))
		if day >= from && day <= through {
			result[day] += amount
		}
	}
	return result
}
