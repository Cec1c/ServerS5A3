package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildHongbaoClaimStatePreservesOtherAccountsShares(t *testing.T) {
	s, sender, repos := hongbaoBridgeFixture(t)
	now := s.gameplayNow()
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	ctx := context.Background()
	if ack := hongbaoBridgeSend(t, s, sender, 64, currentCeraShopTestRequestBody(103941), 200); ack[0] != 1 {
		t.Fatalf("purchase %x", ack)
	}
	ack := hongbaoBridgeSend(t, s, sender, 1303, append(hongbaoBridgeID(490003266), 5), 201)
	if len(ack) != 13 || ack[0] != 1 {
		t.Fatalf("issue %x", ack)
	}
	id := binary.LittleEndian.Uint32(ack[9:])
	list := func(peer *gameSession, portions, state byte) {
		t.Helper()
		got := hongbaoBridgeSend(t, s, peer, 1300, nil, 202)
		if len(got) != 55 || got[0] != 1 || binary.LittleEndian.Uint32(got[15:]) != id || got[53] != portions || got[54] != state {
			t.Fatalf("portions=%d state=%d: %x", portions, state, got)
		}
	}
	list(sender, 5, 0)
	var total uint32
	for i, characterID := range []uint16{29, 30, 33, 100, 101} {
		peer := s.gameSessions[characterID]
		if peer == nil {
			actor, found, err := repos.Character.Load(ctx, fmt.Sprint(characterID))
			if err != nil || !found {
				t.Fatal("character", err)
			}
			peer = &gameSession{conn: &bufferConn{}, accountID: actor.AccountID, selectedCharacterID: characterID, characterGeneration: uint64(characterID), channel: sender.channel, residentChannel: sender.channel}
			s.gameSessions[characterID] = peer
		}
		list(peer, byte(5-i), 0)
		ack = hongbaoBridgeSend(t, s, peer, 1304, hongbaoBridgeID(id), 210)
		if len(ack) != 13 || ack[0] != 1 {
			t.Fatalf("claim %d: %x", characterID, ack)
		}
		total += binary.LittleEndian.Uint32(ack[9:])
		state := byte(2)
		if i == 4 {
			state = 1
		}
		list(peer, byte(4-i), state)
		list(sender, byte(4-i), state)
		if duplicate := hongbaoBridgeSend(t, s, peer, 1304, hongbaoBridgeID(id), 211); duplicate[0] != 0 {
			t.Fatalf("same account took twice: %x", duplicate)
		}
		list(peer, byte(4-i), state)
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil || total != 5000 || reg.Hongbao.Records[id].RemainingPortions() != 0 || len(reg.Hongbao.Records[id].Claims) != 5 {
		t.Fatal("value/claims conservation", total, err)
	}
}

func TestGuildHongbaoCompletedOverridesPersonalClaimState(t *testing.T) {
	for _, completed := range []bool{false, true} {
		body, err := buildCurrentGuildHongbaoListBody(30, 5000, 1, []currentGuildHongbaoRow{{ID: 1, ItemID: 490003266, Name: "Sender", Portions: 4, Claimed: true, Completed: completed}})
		want := byte(2)
		if completed {
			want = 1
		}
		if err != nil || body[53] != want || body[52] != 4 {
			t.Fatalf("completed=%v: %x %v", completed, body, err)
		}
	}
}
