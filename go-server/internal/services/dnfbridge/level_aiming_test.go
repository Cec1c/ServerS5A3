package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPortedLevelAimingNativeRegisterReserveAndReconnect(t *testing.T) {
	for _, test := range []struct {
		upper bool
		slot  int
	}{{true, 0}, {false, 0}, {true, 31}, {false, 31}} {
		ctx := context.Background()
		now := time.Now()
		repos := memory.NewMemoryGroup()
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
		ch := dnfrepo.CharacterRecord{CharacterID: "23", AccountID: "a", Slot: test.slot, Name: "EventRole", Job: "15", Level: 1, CreatedAt: now}
		if err := repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		for _, other := range []dnfrepo.CharacterRecord{
			{CharacterID: "24", AccountID: "a", Slot: 23, Level: 1},
			{CharacterID: "36", AccountID: "a", Slot: 1, Level: 1},
			{CharacterID: "25", AccountID: "b", Slot: 2, Level: 1},
		} {
			if err := repos.Character.Save(ctx, other); err != nil {
				t.Fatal(err)
			}
		}
		c := &dnfonlineevent.LevelAimingCatalog{Limit: 5, MinLevel: 1, MaxLevel: 86, Job: 99, Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour)}
		service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, levelAimingCatalog: c}
		conn := &bufferConn{}
		session := &gameSession{conn: conn, accountID: "a", levelAimingCreatedID: 23}
		enablePortedActivityTest(t, service, session)
		request := func(reference, target uint32) {
			t.Helper()
			conn.write.Reset()
			var w packetWriter
			w.writeUint32(222)
			w.writeUint32(reference)
			w.writeUint32(target)
			w.writeUint32(0)
			var err error
			if test.upper {
				var frame []byte
				frame, err = dnfproto.BuildChannelPacket(689, w.bytes(), 0, dnfproto.DefaultChannelClassification)
				if err == nil {
					err = service.handleGameUpper(session, frame)
				}
			} else {
				err = service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), 689, w.bytes())
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		rejected := func(reference, target uint32) {
			t.Helper()
			request(reference, target)
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 689 || len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, 1, 222, 0, 0, 0, 1}) {
				t.Fatalf("reference=%d target=%d rejection=%x", reference, target, conn.write.Bytes())
			}
		}
		request(24, 255)
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(rest) != 0 || !bytes.Equal(ack.Body, []byte{0, 1, 222, 0, 0, 0, 1}) {
			t.Fatalf("rejection=%x", conn.write.Bytes())
		}
		request(23, 255)
		ack, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1, 222, 0, 0, 0}) {
			t.Fatalf("ack=%x", ack.Body)
		}
		roster, rest := splitGameServerUpperPacket(t, rest)
		if roster.Header.MsgID != 2 {
			t.Fatalf("roster=%d", roster.Header.MsgID)
		}
		progress, rest := splitGameServerUpperPacket(t, rest)
		if len(rest) != 0 || progress.Header.MsgID != 623 || !bytes.Equal(progress.Body, []byte{23, 0, 0, 0, 1, 255, 0, 0, 15, 0, 3, 0}) {
			t.Fatalf("progress=%x", progress.Body)
		}
		saved, _, _ := repos.Character.Load(ctx, "23")
		var row packetWriter
		writeNoPackRosterEntry(&row, 0, saved)
		if row.bytes()[len(row.bytes())-14] != 5 {
			t.Fatalf("unreserved event flag=%x", row.bytes())
		}
		for _, reference := range []uint32{23, 1, 2, defaultCharacterSlots, 65536} {
			rejected(reference, 20)
		}
		request(uint32(test.slot), 20)
		ack, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
		if ack.Header.MsgID != 689 || !bytes.Equal(ack.Body, []byte{1, 222, 0, 0, 0}) {
			t.Fatalf("slot=%d reservation ack=%x", test.slot, ack.Body)
		}
		progress, rest = splitGameServerUpperPacket(t, rest)
		if len(rest) != 0 || progress.Header.MsgID != 623 || !bytes.Equal(progress.Body, []byte{23, 0, 0, 0, 1, 20, 0, 0, 15, 0, 3, 1}) {
			t.Fatalf("reservation auto-enter response=%x", progress.Body)
		}
		request(uint32(test.slot), 20)
		account, _, _ := repos.Account.Load(ctx, "a")
		state, err := dnfonlineevent.ReadLevelAimingState(account)
		if err != nil || state.Target != 20 || state.Reservations != 1 {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		session = &gameSession{conn: conn, accountID: "a", selectedCharacterID: 23}
		enablePortedActivityTest(t, service, session)
		conn.write.Reset()
		if err := service.sendCurrentLevelAiming(session); err != nil {
			t.Fatal(err)
		}
		progress, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(rest) != 0 || !bytes.Equal(progress.Body, []byte{23, 0, 0, 0, 1, 20, 0, 0, 15, 0, 3, 1}) {
			t.Fatalf("reconnect=%x", progress.Body)
		}
		saved, _, _ = repos.Character.Load(ctx, "23")
		row = packetWriter{}
		writeNoPackRosterEntry(&row, 0, saved)
		if row.bytes()[len(row.bytes())-14] != 3 {
			t.Fatalf("reserved flag=%x", row.bytes())
		}
		selector := buildCurrentLevelAimingBody(state, saved, true)
		if selector[10] != 1 || selector[5] != 20 {
			t.Fatalf("selector auto-enter guard=%x", selector)
		}
		ordinary := buildCurrentLevelAimingBody(state, dnfrepo.CharacterRecord{CharacterID: "24"}, false)
		if ordinary[10] != 255 || ordinary[5] != 255 || binary.LittleEndian.Uint32(ordinary) != 23 {
			t.Fatalf("ordinary=%x", ordinary)
		}
		// Town reservations still carry the selector's slot, bound to the active character.
		session.selectedCharacterID = 24
		rejected(uint32(test.slot), 30)
		session.selectedCharacterID = 23
		rejected(23, 30)
		rejected(uint32(test.slot), 30)
		saved.Level = 20
		if err := repos.Character.Save(ctx, saved); err != nil {
			t.Fatal(err)
		}
		request(uint32(test.slot), 30)
		account, _, _ = repos.Account.Load(ctx, "a")
		state, err = dnfonlineevent.ReadLevelAimingState(account)
		if err != nil || state.CharacterID != "23" || state.Start != 21 || state.Target != 30 || state.Reservations != 2 {
			t.Fatalf("town state=%+v err=%v", state, err)
		}
		for _, id := range []int{24, 25, 36} {
			other, _, _ := repos.Character.Load(ctx, strconv.Itoa(id))
			if other.Stats[dnfonlineevent.LevelAimingRoleStat] != 0 {
				t.Fatalf("unrelated character changed: %+v", other)
			}
		}
	}
}

func TestPortedLevelAimingRealPVFRewards(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF smoke path is unset")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfonlineevent.LoadLevelAimingCatalog(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Job != 8 || catalog.Limit != 5 || len(catalog.Stages) != 16 {
		t.Fatalf("catalog=%+v", catalog)
	}
	service := &Service{initialEquipmentArchive: archive}
	items, err := service.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	rewards := []dnfonlineevent.AttendancePVFItemReward{catalog.Main}
	for _, row := range catalog.Stages {
		rewards = append(rewards, row.Item)
	}
	for _, reward := range rewards {
		item, err := items.ResolveItem(uint32(reward.ItemID))
		if err != nil || item.Kind != dungeonDropItemStackable || (!item.ExpirationDate.IsZero() && !item.ExpirationDate.After(catalog.Ends)) {
			t.Fatalf("reward=%+v item=%+v err=%v", reward, item, err)
		}
	}
}
