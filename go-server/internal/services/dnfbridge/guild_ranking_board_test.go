package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildRankingBoardNativeInitializationRoutes(t *testing.T) {
	s, peers, _, _ := rankingBridgeFixture(t)
	// V171 02983710 emits this request during character initialization.
	guildDispatch(t, s, peers[0], 1504, []byte{1, 0, 1, 0})
	body := guildReply(t, peers[0], 1504)
	if len(body) != 6 || body[0] != 1 || body[1] != 1 || binary.LittleEndian.Uint16(body[4:]) != 0 {
		t.Fatalf("empty historical board reply=%x", body)
	}
}

func TestGuildRankingBoardSettledPacketsReadOnlyAndExport(t *testing.T) {
	s, peers, repos, q := rankingBridgeFixture(t)
	ctx := context.Background()
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	lowModel := guildReply(t, peers[0], 1503)
	reg, _ := guild.Load(ctx, repos.Settings)
	previous, err := guild.BuildRankingPeriod(q.now.AddDate(0, -1, 0), map[uint32]guild.RankingFactors{
		1: {Members: 25.75, WeeklyActive: 10.5, Dungeon: 60, War: 35, PVP: 15, Contents: 20, Adventure: 2000, PartyClear: 350, RaidClear: 45},
		2: {Members: 20, WeeklyActive: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := guild.BuildRankingPeriod(q.now, map[uint32]guild.RankingFactors{1: {Members: 50.5, WeeklyActive: 30, Dungeon: 90, War: 100, PVP: 40, Contents: 50, Adventure: 4000, PartyClear: 700, RaidClear: 90}, 2: {Members: 10}})
	reg.Ranking = &guild.RankingState{Current: current, Previous: previous}
	if err = guild.Save(ctx, repos.Settings, reg, q.now); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	packets := map[string]string{"low_model": hex.EncodeToString(lowModel)}
	for name, request := range map[string][]byte{"podium": {1, 0, 1, 0}, "list": {0, 0, 1, 0}, "empty_page": {0, 0, 2, 0}} {
		guildDispatch(t, s, peers[0], 1504, request)
		body := guildReply(t, peers[0], 1504)
		count := 2
		if name == "empty_page" {
			count = 0
		}
		if len(body) != 6+72*count || body[0] != 1 || body[1] != request[0] || int(binary.LittleEndian.Uint16(body[4:])) != count {
			t.Fatalf("%s %x", name, body)
		}
		packets[name] = hex.EncodeToString(body)
		if count > 0 && (binary.LittleEndian.Uint32(body[6:]) != 1 || binary.LittleEndian.Uint32(body[10:]) != 1) {
			t.Fatal("wrong board identity")
		}
	}
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	packets["model"] = hex.EncodeToString(guildReply(t, peers[0], 1503))
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("board/model query changed persisted snapshot")
	}
	// An unguilded current character can read the public board.
	delete(reg.Guilds, 2)
	if err = guild.Save(ctx, repos.Settings, reg, q.now); err != nil {
		t.Fatal(err)
	}
	guildDispatch(t, s, peers[2], 1504, []byte{1, 0, 1, 0})
	if got := guildReply(t, peers[2], 1504); len(got) != 78 || got[0] != 1 {
		t.Fatalf("unguilded reader=%x", got)
	}
	q.now = q.now.AddDate(0, 2, 0)
	guildDispatch(t, s, peers[0], 1504, []byte{1, 0, 1, 0})
	packets["empty_month"] = hex.EncodeToString(guildReply(t, peers[0], 1504))
	if path := os.Getenv("GUILD_BOARD_WIRE_OUTPUT"); path != "" {
		out, err := json.MarshalIndent(packets, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, out, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuildRankingBoardRejectsMalformedAndStaleIdentity(t *testing.T) {
	for _, mode := range []string{"disabled", "short", "extra", "kind", "filter", "page_zero", "podium_page", "stale", "account", "unselected"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := rankingBridgeFixture(t)
			request := []byte{1, 0, 1, 0}
			switch mode {
			case "disabled":
				s.options.guildRankingEnabled = false
			case "short":
				request = request[:3]
			case "extra":
				request = append(request, 0)
			case "kind":
				request[0] = 2
			case "filter":
				request[1] = 1
			case "page_zero":
				request[2] = 0
			case "podium_page":
				request[2] = 2
			case "stale":
				s.gameSessions[1] = peers[2]
			case "account":
				peers[0].accountID = "foreign"
			case "unselected":
				peers[0].selectedCharacterID = 0
			}
			before, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			guildDispatch(t, s, peers[0], 1504, request)
			if got := guildReply(t, peers[0], 1504); len(got) != 2 || got[0] != 0 {
				t.Fatalf("accepted %x", got)
			}
			after, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed query wrote state")
			}
		})
	}
}

func TestGuildRankingBoardNamesKeepMultibyteNULTerminator(t *testing.T) {
	for _, name := range []string{strings.Repeat("会", 20), strings.Repeat("😀", 10), strings.Repeat("a", 23), "a\x00b"} {
		buf := make([]byte, 23)
		writeGuildRankingBoardName(buf, name)
		end := 0
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if end == len(buf) {
			t.Fatal("missing terminator")
		}
		if _, err := decodeCharacterName(buf[:end]); err != nil {
			t.Fatal("split client character", err)
		}
	}
}
