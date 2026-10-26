package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestUnavailableJoustKeepsCharacterAssetsAcrossReconnectAndAllPhases(t *testing.T) {
	ctx := context.Background()
	f := newJoustEventSwitchFixture(t, joustEventSwitchEnabled)
	before, found, err := f.repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	previous, err := f.service.joustCatalog.TournamentFor(f.round - 1)
	if err != nil {
		t.Fatal(err)
	}
	before.Stats = map[string]int64{
		dnfjoust.RoundStat:   int64(f.round - 1),
		dnfjoust.KnightStat:  int64(previous.Champion()),
		dnfjoust.AmountStat:  3,
		dnfjoust.PendingStat: 1,
	}
	if err := f.repositories.Character.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	before, _, _ = f.repositories.Character.Load(ctx, "19")
	bagBefore, _, err := f.repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	f.service.joustCatalog = nil
	f.service.pvfItemCatalog = &pvfDungeonDropCatalog{source: bridgePVFSource{
		dnfjoust.EventPVFPath: strings.Replace(testCurrentJoustPVFText(), "[max betting]\n10000", "[max betting]\n1000", 1),
	}}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	// The same process can select another character without resending op108.
	for _, first := range []bool{true, false, true} {
		f.connection.write.Reset()
		sent, err := f.service.sendCurrentSpendTimeProtocolState(f.session, catalog, 0, 0, first)
		if err != nil || sent != first {
			t.Fatalf("descriptor=%t sent=%t err=%v", first, sent, err)
		}
		packets := splitAllGameServerUpperPackets(t, f.connection.write.Bytes())
		want := 1
		if first {
			want++
		}
		if len(packets) != want || packets[len(packets)-1].Header.MsgID != currentSpendTimeProgressMsgID {
			t.Fatalf("unexpected reconnect packets: %+v", packets)
		}
	}
	f.connection.write.Reset()
	base := time.Unix(7200*int64(f.round), 0).UTC()
	for _, offset := range []time.Duration{0, dnfjoust.BettingDuration, dnfjoust.QuarterFinalEnd, dnfjoust.SemiFinalEnd, dnfjoust.SettlementStart} {
		now := base.Add(offset)
		if err := f.service.pushCurrentJoustBoundarySnapshot(ctx, f.session, dnfjoust.TimelineAt(now)); err != nil {
			t.Fatalf("phase %s: %v", offset, err)
		}
		f.service.handleCurrentJoustBoundary(now)
	}
	if f.connection.write.Len() != 0 {
		t.Fatalf("unavailable activity emitted phase packets: %x", f.connection.write.Bytes())
	}
	// A stale client window can still submit a bet. Keep the original rejection.
	if err := f.service.handleCurrentJoustBetting(f.session, []byte{1, 7, 0, 0, 0, 1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, f.connection.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketJoustBetting) || len(rest) != 0 || len(packet.Body) != 5 || packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[1:]) != currentJoustBettingResultInvalid {
		t.Fatalf("bet rejection header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
	after, _, err := f.repositories.Character.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("character changed: before=%+v after=%+v err=%v", before, after, err)
	}
	bagAfter, _, err := f.repositories.Inventory.Load(ctx, "19")
	if err != nil || !reflect.DeepEqual(bagBefore, bagAfter) {
		t.Fatalf("inventory changed: before=%+v after=%+v err=%v", bagBefore, bagAfter, err)
	}
	mailbox, _, err := f.repositories.Mailbox.Load(ctx, "19")
	if err != nil || len(mailbox.Mails) != 0 {
		t.Fatalf("incompatible catalog settled old reward: mailbox=%+v err=%v", mailbox, err)
	}
}

func TestForcedJoustCatalogStillRequiresCompatibleResources(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "compatible"}[valid], func(t *testing.T) {
			f := newJoustEventSwitchFixture(t, joustEventSwitchDisabled)
			f.service.options.joustEventCatalogMode = currentSpendTimeJoustCatalogOn
			if !valid {
				f.service.joustCatalog = nil
				f.service.pvfItemCatalog = &pvfDungeonDropCatalog{source: bridgePVFSource{}}
			}
			catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
			if _, err := f.service.sendCurrentSpendTimeProtocolState(f.session, catalog, 0, 0, true); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, f.connection.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
			body, err := zlibDecompress(packet.Body)
			if err != nil {
				t.Fatal(err)
			}
			if shape := parseCurrentSpendTimeEventInfoBody(t, body); shape.hasEvent(uint16(currentJoustEventID)) != valid {
				t.Fatalf("valid=%t catalog=%+v", valid, shape)
			}
			progress, rest := splitGameServerUpperPacket(t, rest)
			if progress.Header.MsgID != currentSpendTimeProgressMsgID || len(rest) != 0 {
				t.Fatalf("disabled event emitted pushes: progress=%+v rest=%x", progress.Header, rest)
			}
		})
	}
}
