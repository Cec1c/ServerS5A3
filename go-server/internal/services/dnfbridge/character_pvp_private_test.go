package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFPrivateCharacterAllProfessions(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	s := &Service{options: options{accountPrefix: "dnf:"}, initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	if err := s.preloadCharacterStatTable(ctx); err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText("etc/pvpparameter.etc")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("etc/pvpparameter.etc", text)
	if err != nil {
		t.Fatal(err)
	}
	pairs := doc.Ints("pvp matching possible character")
	for i := 0; i < len(pairs); i += 2 {
		job, grow := byte(pairs[i]), byte(pairs[i+1])
		t.Run(fmt.Sprintf("job%d-grow%d", job, grow), func(t *testing.T) {
			repos = memory.NewMemoryGroup()
			logPath := filepath.Join(t.TempDir(), "creation.log")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			s.packetLog = logger
			t.Cleanup(func() {
				_ = logger.Close()
				if t.Failed() {
					data, _ := os.ReadFile(logPath)
					t.Logf("creation log:\n%s", data)
				}
			})
			plan, err := s.loadPVPPrivateCreation(job, grow)
			if err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			// 注：参考实现此处还断言 session.levelAimingCreatedID == 0（专用角色不参与
			// 预约升级）。本树没有预约升级链路，gameSession 也无该字段，故只测专用角色本身。
			session := &gameSession{conn: conn}
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body, uint32(job))
			binary.LittleEndian.PutUint32(body[4:], uint32(grow))
			if err := s.handleGameCommand(session, 1, 870, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 870 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 0}) || len(rest) != 0 {
				t.Fatalf("precheck=%+v %x", ack.Header, ack.Body)
			}
			conn.write.Reset()
			frame, err := dnfproto.BuildChannelPacket(870, body, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(session, frame); err != nil {
				t.Fatal(err)
			}
			ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{1, 0}) {
				t.Fatalf("upper precheck=%x", ack.Body)
			}
			conn.write.Reset()
			name := fmt.Sprintf("pvptest%dg%d", job, grow)
			request := append(buildCreateRequest(job, name), 0, 0, 0, 0, 0, 0, grow, 0)
			if err := s.handleUpperCreateCharacter(session, request); err != nil {
				t.Fatal(err)
			}
			ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 5 || len(ack.Body) < 3 || ack.Body[0] != 1 {
				t.Fatalf("create=%x", ack.Body)
			}
			id := fmt.Sprint(binary.LittleEndian.Uint16(ack.Body[1:3]))
			c, ok, err := repos.Character.Load(ctx, id)
			if err != nil || !ok {
				t.Fatalf("load %s: %v", id, err)
			}
			if c.Level != plan.Level || c.Job != fmt.Sprint(job) || c.Stats["grow_type"] != int64(grow) || !dnfpvp.IsPrivateCharacter(c) || c.Stats["tutorial_completed"] != 1 {
				t.Fatalf("character=%+v", c)
			}
			if c.Stats["stat_hp_max"] <= 0 || c.Stats["stat_mp_max"] <= 0 {
				t.Fatalf("private stats: %+v", c.Stats)
			}
			equip, _, err := repos.Equipment.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			bag, _, err := repos.Inventory.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			wantEquip, wantBag := 0, 0
			for _, item := range plan.Equipment {
				if item.Equipped {
					wantEquip++
					slot := initialEquipmentSlotMap[item.Slot]
					if equip.Entries[fmt.Sprint(slot)].ItemID != item.ItemID {
						t.Fatalf("missing equipped %+v", item)
					}
				} else {
					wantBag++
					if bag.Slots[fmt.Sprintf("0:%d", 8+wantBag)].ItemID != item.ItemID {
						t.Fatalf("missing bag %+v", item)
					}
				}
			}
			if len(equip.Entries) != wantEquip || len(bag.Slots) != wantBag+1 {
				t.Fatalf("equipment=%d bag=%d", len(equip.Entries), len(bag.Slots))
			}
			owner, err := dnfequip.NewOwnerWithPlacementValidator(repos, dnfequip.PlacementValidatorFunc(func(context.Context, dnfequip.Placement) error { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			for key, stack := range bag.Slots {
				if stack.Extra["source"] != "pvf_pvp_private_create_equipment_list" {
					continue
				}
				if len(stack.RawEntry) != 119 {
					t.Fatalf("spare %s has no complete item row: %x", key, stack.RawEntry)
				}
				_, slot, _ := parseSceneInventorySlotKey(key)
				cmd := dnfequip.MoveCommand{SelectedCharacterID: binary.LittleEndian.Uint16(ack.Body[1:3]), SourceListType: 0, SourceSlotIndex: slot,
					SourceInstanceValue: int32(stack.ItemID), MoveCount: 1, DestinationListType: 3, DestinationSlotIndex: 12}
				if _, err := owner.Move(ctx, cmd); err != nil {
					t.Fatalf("equip spare %s: %v", key, err)
				}
				cmd.SourceSlotIndex = 63
				cmd.SourceInstanceValue, cmd.DestinationInstanceValue = 0, int32(stack.ItemID)
				if _, err := owner.Move(ctx, cmd); err != nil {
					t.Fatalf("unequip spare %s: %v", key, err)
				}
				cmd.SourceInstanceValue, cmd.DestinationInstanceValue = int32(stack.ItemID), 0
				if _, err := owner.Move(ctx, cmd); err != nil {
					t.Fatalf("reequip spare %s: %v", key, err)
				}
			}
			skill, ok, err := repos.Skill.Load(ctx, id)
			if err != nil || !ok {
				t.Fatalf("skill: %v", err)
			}
			for _, tree := range []int{dnfrepo.ActiveSkillTree(skill)} {
				state, ok := dnfrepo.SkillTree(skill, tree)
				if !ok || len(state.Skills) == 0 || state.Points.TotalSP <= 0 {
					t.Fatalf("skill tree%d=%+v", tree, state)
				}
			}
			if _, err := plan.Check(ctx, repos.Character, c.AccountID, time.Now()); err != dnfpvp.PrivateCreateError(150) {
				t.Fatalf("duplicate check=%v", err)
			}
			conn.write.Reset()
			if err := s.handleCheckPVPPrivateCharacter(session, body); err != nil {
				t.Fatal(err)
			}
			ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 150}) {
				t.Fatalf("duplicate ack=%x", ack.Body)
			}
			t.Logf("level=%d equipment=%d bag=%d SP=%d skills=%d", c.Level, wantEquip, wantBag, skill.Points.TotalSP, len(skill.Skills))
		})
	}
	t.Run("ordinary-six-options", func(t *testing.T) {
		repos = memory.NewMemoryGroup()
		conn := &bufferConn{}
		session := &gameSession{conn: conn}
		request := append(buildCreateRequest(0, "ordinary"), 0, 0, 0, 0, 0, 255)
		if err := s.handleUpperCreateCharacter(session, request); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(ack.Body) < 3 || ack.Body[0] != 1 {
			t.Fatalf("ordinary create=%x", ack.Body)
		}
		c, ok, err := repos.Character.Load(ctx, "1")
		// 参考实现还断言 session.levelAimingCreatedID == 1（普通角色登记预约升级）。
		// 本树没有预约升级链路，故只断言"这是普通角色"。
		if err != nil || !ok || c.Level != newCharacterInitialLevel || dnfpvp.IsPrivateCharacter(c) || c.Stats["grow_type"] != 0 {
			t.Fatalf("ordinary=%+v err=%v", c, err)
		}
	})
}
