package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/repository"
)

func setPartyEntryRefusalFatigue(t *testing.T, fixture *doc104PartyFixture, characterID string, fatigue int64) {
	t.Helper()
	repositories, ok := fixture.service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	character, found, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
	}
	character.Stats["fatigue"] = fatigue
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
}

func assertPartyEntryRefusalContext(t *testing.T, steps []partyDungeonSyncStep, reason, memberSlot byte) {
	t.Helper()
	op16 := doc104Op16Steps(steps)
	if len(op16) != 1 {
		t.Fatalf("op16 refusal frames=%d want 1: %+v", len(op16), steps)
	}
	want := []byte{0, reason, memberSlot}
	if op16[0].Classification != dnfproto.DefaultChannelClassification || !bytes.Equal(op16[0].Body, want) {
		t.Fatalf("op16 refusal class=%d body=% X want class=%d body=% X",
			op16[0].Classification, op16[0].Body, dnfproto.DefaultChannelClassification, want)
	}
}

func TestDungeonEntryRefusalOwnerSlotForLeaderAndMemberFatigue(t *testing.T) {
	t.Run("leader fatigue uses leader slot", func(t *testing.T) {
		fixture := newDoc104PartyFixture(t, 20, true)
		setPartyEntryRefusalFatigue(t, fixture, "99", 0)
		fixture.pressEnter(t, fixture.leader)
		assertPartyEntryRefusalContext(t, doc104StepsFor(t, fixture, 99), currentSelectDungeonInsufficientFatigueReason, 0)
		if steps := doc104StepsFor(t, fixture, 100); len(steps) != 0 {
			t.Fatalf("idle member received leader-owned refusal: %+v", steps)
		}
	})

	t.Run("leader request names the insufficient member slot", func(t *testing.T) {
		fixture := newDoc104PartyFixture(t, 20, true)
		setPartyEntryRefusalFatigue(t, fixture, "100", 0)
		fixture.pressEnter(t, fixture.leader)
		assertPartyEntryRefusalContext(t, doc104StepsFor(t, fixture, 99), currentSelectDungeonInsufficientFatigueReason, 1)
		if steps := doc104StepsFor(t, fixture, 100); len(steps) != 0 {
			t.Fatalf("idle member received an unsolicited refusal: %+v", steps)
		}
	})

	t.Run("routed member request carries the same member owner", func(t *testing.T) {
		fixture := newDoc104PartyFixture(t, 20, true)
		setPartyEntryRefusalFatigue(t, fixture, "100", 0)
		fixture.pressEnter(t, fixture.follower)
		waitDoc104(t, "the routed member fatigue refusal", func() bool {
			return len(doc104Op16Steps(doc104StepsFor(t, fixture, 100))) >= 2 &&
				len(doc104Op16Steps(doc104StepsFor(t, fixture, 99))) >= 1
		})
		followerOP16 := doc104Op16Steps(doc104StepsFor(t, fixture, 100))
		if len(followerOP16) != 2 || !bytes.Equal(followerOP16[1].Body, []byte{0, currentSelectDungeonInsufficientFatigueReason, 1}) {
			t.Fatalf("follower op16 ACK/refusal mismatch: %+v", followerOP16)
		}
		assertPartyEntryRefusalContext(t, doc104Op16Steps(doc104StepsFor(t, fixture, 99)), currentSelectDungeonInsufficientFatigueReason, 1)
	})
}

func TestDungeonEntryRefusalOwnerSlotForLeaderAndMemberRequiredItem(t *testing.T) {
	t.Setenv(dungeonEntryRequiredItemReasonEnv, "")
	state := alignedcmd.PartyState{
		PartyID: 99,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, Slot: 2},
			{UserID: 100, Slot: 1},
		},
	}
	for _, test := range []struct {
		name       string
		character  uint16
		memberSlot byte
		err        error
	}{
		{name: "leader required item uses fixed slot", character: 99, memberSlot: 2, err: dnfdungeon.ErrEntryRequiredItemMissing},
		{name: "member required item uses fixed slot", character: 100, memberSlot: 1, err: dnfdungeon.ErrEntryRequiredItemMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session := newDungeonSelectRefusalFixture(t, repository.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 20,
				Stats: map[string]int64{"fatigue": 100},
			}, nil)
			request := dungeoncmd.SelectDungeonRequest{DungeonID: 700}
			if slot, ok := runtimePartyMemberIndex(test.character, state); !ok || slot != test.memberSlot {
				t.Fatalf("existing protocol party index=%d ok=%t want %d", slot, ok, test.memberSlot)
			}
			err := wrapPartyDungeonEntryMemberFailure(state, test.character, test.err)
			if err := service.refuseDungeonSelectEntryValidation(session, request, err); err != nil {
				t.Fatal(err)
			}
			packet, trailing := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			want := []byte{0, currentSelectDungeonMissingRequiredItemReason, test.memberSlot}
			if packet.Header.Classification != dnfproto.DefaultChannelClassification || len(trailing) != 0 || !bytes.Equal(packet.Body, want) {
				t.Fatalf("required-item refusal header=%+v body=% X trailing=% X want % X",
					packet.Header, packet.Body, trailing, want)
			}
		})
	}
}

func TestDungeonEntryRefusalOwnerSlotLeavesExistingLevelContextUnchanged(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID: 99,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, Slot: 0},
			{UserID: 100, Slot: 1},
		},
	}
	service, session := newDungeonSelectRefusalFixture(t, repository.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"fatigue": 100},
	}, nil)
	err := wrapPartyDungeonEntryMemberFailure(state, 100, dnfdungeon.ErrEntryLevelTooLow)
	if err := service.refuseDungeonSelectEntryValidation(session, dungeoncmd.SelectDungeonRequest{DungeonID: 700}, err); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	want := []byte{0, currentSelectDungeonEntryLevelTooLowReason, 0}
	if packet.Header.Classification != dnfproto.DefaultChannelClassification || len(trailing) != 0 || !bytes.Equal(packet.Body, want) {
		t.Fatalf("non-cost refusal header=%+v body=% X trailing=% X want % X",
			packet.Header, packet.Body, trailing, want)
	}
}
