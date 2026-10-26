package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildRankingWireNativeLayout(t *testing.T) {
	rows := [3]guild.RankingRow{
		{Rank: 3, Factors: guild.RankingFactors{Members: 12.3, WeeklyActive: 8.5, Dungeon: 26, War: 4, PVP: 31, Contents: 20, Adventure: 27.5, PartyClear: 9, Event: 1, RaidClear: 3}},
		{Rank: 7, Factors: guild.RankingFactors{Members: 10, WeeklyActive: 6, Dungeon: 13, War: 2, PVP: 18, Contents: 10, Adventure: 21, PartyClear: 2, Event: 0, RaidClear: 1}},
		{Rank: 9, Factors: guild.RankingFactors{Members: 8}},
	}
	for i := range rows {
		rows[i].Score, _ = rows[i].Factors.Total()
	}
	body, err := buildCurrentGuildRankingBody(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 108 || binary.LittleEndian.Uint32(body) != 0 {
		t.Fatal("wrong model header")
	}
	for i, row := range rows {
		if binary.LittleEndian.Uint32(body[4+i*8:]) != row.Rank || math.Float32frombits(binary.LittleEndian.Uint32(body[8+i*8:])) != row.Score {
			t.Fatalf("period %d", i)
		}
	}
	// Native page case order differs from both wire field order and total order.
	for offset, want := range map[int]float32{28: 12.3, 32: 8.5, 36: 26, 40: 4, 44: 31, 48: 20, 52: 10, 56: 6, 60: 13, 64: 2, 68: 18, 72: 10, 76: 27.5, 80: 9, 84: 1, 88: 3, 92: 21, 96: 2, 100: 0, 104: 1} {
		if got := math.Float32frombits(binary.LittleEndian.Uint32(body[offset:])); got != want {
			t.Fatalf("offset=%d got=%v want=%v", offset, got, want)
		}
	}
	if path := os.Getenv("GUILD_RANKING_WIRE_OUTPUT"); path != "" {
		data, err := json.MarshalIndent(struct {
			BodyHex string
			Rows    [3]guild.RankingRow
		}{hex.EncodeToString(body), rows}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rows[0].Score++
	if _, err = buildCurrentGuildRankingBody(rows); err == nil {
		t.Fatal("accepted inconsistent total")
	}
}

func TestGuildRankingWireAbsentPeriodsClearOldModel(t *testing.T) {
	body, err := buildCurrentGuildRankingBody([3]guild.RankingRow{})
	if err != nil || len(body) != 108 {
		t.Fatal(err)
	}
	for _, v := range body {
		if v != 0 {
			t.Fatal("empty snapshot has old data")
		}
	}
}
