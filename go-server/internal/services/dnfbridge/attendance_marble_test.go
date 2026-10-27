package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestParseAttendanceMarbleCatalogRequiresRealBoardShape(t *testing.T) {
	documentText := "[reward postal day]\n15\n[/reward postal day]\n[reward clear]\n4 2 8 2 12 2\n[/reward clear]\n[accumulate race info]\n4 490008906 1 1591 1593 8 490008907 1 1591 1594 12 490008908 1 1591 1595\n[/accumulate race info]\n"
	for index := 0; index < attendanceMarbleBoardSize; index++ {
		documentText += fmt.Sprintf("[slot]\n[index]\n%d\n[type]\n1\n[item]\n490008905 1\n[/slot]\n", index)
	}
	document, err := parsePVFForTest(documentText)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseAttendanceMarbleCatalog(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.StageRewards) != 3 || catalog.Slots[27].ItemID != 490008905 {
		t.Fatalf("catalog=%+v", catalog)
	}
}

func TestAttendanceMarbleProtocolBodiesHaveVerifiedWidths(t *testing.T) {
	state := attendanceMarbleState{Position: 27, Round: 12, Turns: 8, DiceUsed: 2, Double: 1, CalendarDay: 20260809}
	info := buildAttendanceMarbleInfoBody(state)
	if len(info) != 16 || binary.LittleEndian.Uint16(info[:2]) != 0 || binary.LittleEndian.Uint16(info[2:]) != 8 || binary.LittleEndian.Uint16(info[4:]) != 6 || binary.LittleEndian.Uint16(info[6:]) != 27 || binary.LittleEndian.Uint32(info[8:]) != 0 || binary.LittleEndian.Uint32(info[12:]) != 12 {
		t.Fatalf("info body=%x", info)
	}
	dice := buildAttendanceMarbleDiceBody([2]byte{6, 3}, 1, 12)
	if len(dice) != 8 || dice[0] != 6 || dice[1] != 3 || binary.LittleEndian.Uint16(dice[2:]) != 1 || binary.LittleEndian.Uint32(dice[4:]) != 12 {
		t.Fatalf("dice body=%x", dice)
	}
	if err := parseAttendanceMarbleRequestInfo(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAttendanceMarbleRequestDice([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAttendanceMarbleRequestDice(nil); err == nil {
		t.Fatal("empty dice request accepted")
	}
}

func TestAttendanceMarbleFreshStateEncodesZeroLaps(t *testing.T) {
	body := buildAttendanceMarbleInfoBody(attendanceMarbleState{CalendarDay: 20260912, Turns: 2})
	if binary.LittleEndian.Uint16(body[2:]) != 2 || binary.LittleEndian.Uint16(body[4:]) != 2 || binary.LittleEndian.Uint32(body[12:]) != 0 {
		t.Fatalf("fresh login needs two earned/remaining turns and zero laps: %x", body)
	}
}

func TestAttendanceMarbleNumericBuffEffects(t *testing.T) {
	mask := byte(2)
	if got := currentAttendanceMarbleMonsterExperienceBonus(mask, 200); got != 200 {
		t.Fatalf("monster buff 30 bonus=%d want=200", got)
	}
	mask |= 8
	if got := currentAttendanceMarbleMonsterExperienceBonus(mask, 200); got != 300 {
		t.Fatalf("monster buffs 30+32 bonus=%d want=300", got)
	}
	if got := currentAttendanceMarbleClearExperienceBonus(mask, 200); got != 200 {
		t.Fatalf("clear buff 32 bonus=%d want=200", got)
	}
	mask |= 4
	if got := currentAttendanceMarbleClearExperienceBonus(mask, 200); got != 400 {
		t.Fatalf("clear buffs 31+32 bonus=%d want=400", got)
	}
	if got := currentAttendanceMarbleMonsterExperienceBonus(0, 200); got != 0 {
		t.Fatalf("inactive monster bonus=%d want=0", got)
	}
}

func TestParseAttendanceMarbleCatalogAgainstRuntimePVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	explicit := path != ""
	if !explicit {
		var err error
		path, err = filepath.Abs(filepath.Join("..", "..", "..", "..", "runtime", "data", "dnf", "Script.pvf"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		if explicit {
			t.Fatalf("requested real PVF unavailable: %v", err)
		}
		t.Skipf("runtime PVF unavailable: %v", err)
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(attendanceMarblePVFPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := dnfpvf.Parse(attendanceMarblePVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseAttendanceMarbleCatalog(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.StageRewards) != 3 || catalog.Slots[7].Type != 3 || catalog.Slots[14].Type != 5 {
		t.Fatalf("runtime attendance catalog shape=%+v", catalog)
	}
}

func parsePVFForTest(text string) (*dnfpvf.Document, error) {
	return dnfpvf.Parse("event/attendancemarble.evt", text)
}

func TestAttendanceMarbleNativeHandlersAndMail(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = time.Date(2026, 9, 12, 12, 0, 0, 0, onlineevent.ChinaCalendar)
			c := &attendanceMarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}, PostalDays: 15}
			for i := range c.Slots {
				c.Slots[i] = onlineevent.MarbleSlot{Index: i, Type: 1, ItemID: 100, ItemCount: 1}
			}
			service := &Service{gameplayTimers: queue, attendanceMarbleCatalog: c, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				pvfItemCatalog: &pvfDungeonDropCatalog{source: bridgePVFSource{}, itemCache: map[uint32]dungeonDropItemDefinition{100: {ItemID: 100, Kind: dungeonDropItemStackable}}}}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, accountID: "a", selectedCharacterID: 7}
			module := attendanceMarbleGameplayModule()
			handlers := module.UpperHandlers
			if legacy {
				handlers = module.LegacyHandlers
			}
			invoke := func(op uint16, body []byte) {
				t.Helper()
				conn.write.Reset()
				if err := handlers[op](service, session, gameplayRequest{Opcode: op, Classification: 1, Body: body}); err != nil {
					t.Fatal(err)
				}
			}
			invoke(629, nil)
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(rest) != 0 || packet.Header.MsgID != 629 || packet.Header.Classification != 1 || len(packet.Body) != 17 || packet.Body[0] != 1 || binary.LittleEndian.Uint16(packet.Body[3:]) != 2 || binary.LittleEndian.Uint16(packet.Body[5:]) != 2 || binary.LittleEndian.Uint32(packet.Body[13:]) != 0 {
				t.Fatalf("fresh native body=%x", packet.Body)
			}
			invoke(630, []byte{0})
			packet, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 630 || len(packet.Body) != 9 || packet.Body[0] != 1 || packet.Body[1] < 1 || packet.Body[1] > 6 || packet.Body[2] < 1 || packet.Body[2] > 6 || binary.LittleEndian.Uint16(packet.Body[3:]) != 1 || binary.LittleEndian.Uint32(packet.Body[5:]) != 0 {
				t.Fatalf("dice native body=%x", packet.Body)
			}
			for len(rest) > 0 {
				p, r := splitGameServerUpperPacket(t, rest)
				rest = r
				if p.Header.MsgID == 629 {
					t.Fatal("info interrupted native dice animation")
				}
			}
			ch, _, _ := repos.Character.Load(ctx, "7")
			if ch.Stats["attendance_marble_position"] != int64(packet.Body[1]+packet.Body[2]) {
				t.Fatal("server and client board position differ")
			}
			mailbox, _, _ := repos.Mailbox.Load(ctx, "7")
			if len(mailbox.Mails) != 1 {
				t.Fatalf("mailbox=%+v", mailbox)
			}
			for _, m := range mailbox.Mails {
				if len(m.Attachments) != 1 || m.Attachments[0].ItemID != 100 || m.Metadata["unlimited"] == "true" || !m.ExpireAt.Equal(queue.now.Add(15*24*time.Hour)) {
					t.Fatalf("reward mail=%+v", m)
				}
			}
			invoke(641, nil)
			if conn.write.Len() != 0 {
				t.Fatal("unrecognized same-op 641 response")
			}
			ch, _, _ = repos.Character.Load(ctx, "7")
			if ch.Stats["attendance_marble_double"] != 0 {
				t.Fatal("double exit not saved")
			}
			ch.Stats["attendance_marble_dice_used"] = 2
			if err := repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			invoke(630, []byte{0})
			packet, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 630 || len(packet.Body) != 2 || packet.Body[0] != 0 {
				t.Fatalf("spent ack=%x", packet.Body)
			}
			packet, rest = splitGameServerUpperPacket(t, rest)
			if packet.Header.MsgID != 629 || packet.Body[0] != 1 || binary.LittleEndian.Uint16(packet.Body[5:]) != 0 || len(rest) != 0 {
				t.Fatal("failed roll did not restore board state")
			}
			mailbox, _, _ = repos.Mailbox.Load(ctx, "7")
			if len(mailbox.Mails) != 1 {
				t.Fatal("rejected roll delivered reward")
			}
		})
	}
}

func TestAttendanceMarbleAllRuntimeRewardsDeliver(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not requested")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := archive.ReadText("ui/event/attendancemarble/attendancemarblewindow_chn.xui"); err != nil {
		t.Fatalf("现用棋盘缺少原生界面资源：%v", err)
	}
	repos := memory.NewMemoryGroup()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 9, 12, 12, 0, 0, 0, onlineevent.ChinaCalendar)
	service := &Service{initialEquipmentArchive: archive, gameplayTimers: queue, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	c, err := service.currentAttendanceMarbleCatalog()
	if err != nil {
		t.Fatal(err)
	}
	buffText, err := archive.ReadText("etc/variousbufflist.etc")
	if err != nil {
		t.Fatal(err)
	}
	buffDoc, err := dnfpvf.Parse("etc/variousbufflist.etc", buffText)
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	name := ""
	effect := false
	seen := map[int64]int{}
	for _, section := range buffDoc.Sections {
		tokens := buffDoc.Tokens[section.Start:section.End]
		if section.Name == "index" && len(tokens) == 1 {
			index = tokens[0].Int
		}
		if index < 29 || index > 32 {
			continue
		}
		switch section.Name {
		case "effect":
			effect = true
		case "/effect":
			effect = false
		case "name", "type":
			if len(tokens) == 1 {
				name = tokens[0].Value
			}
		case "param":
			want := []int64{1, 0}
			if effect {
				switch name {
				case "unique_rarity_item_drop_rate":
					want = []int64{3, onlineevent.MarbleUniqueBonusPercent}
				case "hunter_exp_rate":
					want = []int64{100}
					if index == 32 {
						want[0] = 50
					}
				case "dungeon_clear_exp_rate":
					want = []int64{100}
				default:
					t.Fatalf("unexpected marble effect %s", name)
				}
			} else if name != "dungeon_one_match" {
				t.Fatalf("buff %d lifecycle %s", index, name)
			}
			if len(tokens) != len(want) {
				t.Fatal("buff parameter count differs")
			}
			for i, v := range want {
				if tokens[i].Int != v {
					t.Fatalf("buff %d %s parameter %d=%d want %d", index, name, i, tokens[i].Int, v)
				}
			}
			seen[index]++
		}
	}
	if len(seen) != 4 || seen[29] != 2 || seen[30] != 2 || seen[31] != 2 || seen[32] != 3 {
		t.Fatalf("missing buff definitions: %v", seen)
	}
	session := &gameSession{accountID: "a", selectedCharacterID: 7}
	for i, slot := range c.Slots {
		ch := dnfrepo.CharacterRecord{CharacterID: "7", AccountID: "a", Stats: map[string]int64{
			"gold": 10000, onlineevent.MarbleDayKey: onlineevent.MarbleDay(queue.now), "attendance_marble_position": int64((i + 25) % 28), "attendance_marble_serial": int64(i),
		}}
		if err := repos.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
		s, err := service.settleAttendanceMarble(session, &[2]byte{1, 2}, false)
		if err != nil {
			t.Fatalf("real slot %d: %v", i, err)
		}
		if slot.Type == 3 {
			slot = c.Slots[slot.Goto]
		}
		if s.Position != int64(slot.Index) {
			t.Fatalf("slot %d position=%d", i, s.Position)
		}
		if slot.Type == 1 || slot.Type == 5 {
			mailbox, _, _ := repos.Mailbox.Load(ctx, "7")
			m := mailbox.Mails[s.MailIDs[0]]
			if m.Attachments[0].ItemID != slot.ItemID || m.Attachments[0].Count != slot.ItemCount {
				t.Fatalf("slot %d reward=%+v", i, m)
			}
		}
	}
	ch, _, _ := repos.Character.Load(ctx, "7")
	ch.Stats["attendance_marble_round"] = 12
	ch.Stats["attendance_marble_dice_used"] = 0
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	s, err := service.settleAttendanceMarble(session, &[2]byte{1, 2}, false)
	if err != nil || len(s.MailIDs) != 4 || s.Reward4 != 1 || s.Reward8 != 1 || s.Reward12 != 1 {
		t.Fatalf("real cumulative rewards: %+v %v", s, err)
	}
}

func TestAttendanceMarbleBuffConsumedOnceForWholeDungeon(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	ctx := context.Background()
	runtime.Character.Stats[attendanceMarbleBuffKey(30)] = 1
	if err := repos.Character.Save(ctx, runtime.Character); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{conn: &bufferConn{}, accountID: runtime.Character.AccountID, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	if err := s.sendAttendanceMarbleBuff(session, 30, 2); err != nil {
		t.Fatal(err)
	}
	remove, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	add, rest := splitGameServerUpperPacket(t, rest)
	if remove.Header.MsgID != 488 || add.Header.MsgID != 487 || len(add.Body) != 17 || binary.LittleEndian.Uint32(add.Body[1:]) != 30 || binary.LittleEndian.Uint32(add.Body[5:]) != 2 || len(rest) != 0 {
		t.Fatal("native pending-dungeon count differs")
	}
	if err := s.activateAttendanceMarbleDungeonBuffs(session, runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.attendanceMarbleBuffs != 2 || !runtime.attendanceMarbleBuffsLoaded {
		t.Fatal("entry did not activate pending buff")
	}
	ch, _, _ := repos.Character.Load(ctx, "99")
	if ch.Stats[attendanceMarbleBuffKey(30)] != 0 {
		t.Fatal("pending buff not consumed")
	}
	ch.Stats[attendanceMarbleBuffKey(32)] = 1
	if err := repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := s.activateAttendanceMarbleDungeonBuffs(session, runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.attendanceMarbleBuffs != 2 {
		t.Fatal("room revisit replaced active buffs")
	}
	next := &runtimeDungeonState{Character: ch}
	if err := s.activateAttendanceMarbleDungeonBuffs(session, next); err != nil {
		t.Fatal(err)
	}
	if next.attendanceMarbleBuffs != 8 {
		t.Fatalf("next run buff %d", next.attendanceMarbleBuffs)
	}
}
