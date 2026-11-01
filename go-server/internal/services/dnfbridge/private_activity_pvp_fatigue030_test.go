package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/activeness"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func privateActivityPVPFatigue030Fixture(t *testing.T, cost int64) (*Service, repo.Group) {
	t.Helper()
	s := pvpRoomTestService(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	s.gameplayTimers = queue
	doc, err := pvf.Parse(activeness.PVFPath, `[private activity tasks]
1 1 10 2 1 10 3 1 50 4 1 100 5 1 10 6 100 20 7 1 10 8 1 10
[/private activity tasks]
[private activity rewards]
1 30 500 990027401 1 0 0 2 50 500 990027402 1 3330 500 3 100 5500 990027403 1 3330 500
[/private activity rewards]`)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := activeness.ParseCatalog(doc)
	if err != nil || catalog == nil {
		t.Fatal("activity catalog", err)
	}
	archive := &platformpvf.Archive{}
	s.initialEquipmentArchive = archive
	privateActivityCatalogs.Store(archive, privateActivityCatalogResult{catalog: catalog})
	t.Cleanup(func() { privateActivityCatalogs.Delete(archive) })
	s.pvpFatigueRules = &currentPVPFatigueRules{ArcadeDifficulty: map[byte]int64{0: cost}}
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	for i, id := range []string{"29", "30"} {
		c := privateActivityPVPFatigue030Character(t, g, id)
		// A shared account must not share activity progress between characters.
		c.AccountID, c.UpdatedAt = "pvp-29", queue.now
		c.Stats["fatigue"], c.Stats["fatigue_limit"] = 156, 156
		c.Stats["fatigue_used"], c.Stats["fatigue_display_update"] = 0, 0
		c.Stats[fatigue.DayKey] = premium.ServiceDayID(queue.now)
		if _, err := activeness.Apply(&c, catalog, 6, []uint32{70, 11}[i], queue.now, queue.now); err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		q := repo.QuestRecord{CharacterID: id, States: map[int64]repo.QuestState{}}
		for quest := int64(2670); quest <= 2674; quest++ {
			q.States[quest] = repo.QuestState{Status: "completed"}
		}
		if err := g.Quest.Save(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return s, g
}

func privateActivityPVPFatigue030Character(t *testing.T, g repo.Group, id string) repo.CharacterRecord {
	t.Helper()
	c, found, err := g.Character.Load(context.Background(), id)
	if err != nil || !found {
		t.Fatal("load character", id, found, err)
	}
	return c
}

func privateActivityPVPFatigue030Session(id uint16, difficulty byte) (*gameSession, *currentPVPRoom, *pvpFailNthConn) {
	conn := &pvpFailNthConn{}
	session := &gameSession{conn: conn, accountID: "pvp-29", selectedCharacterID: id}
	room := &currentPVPRoom{id: id, battleMode: 10, request: currentPVPMakeRoomRequest{dojoDifficulty: difficulty}}
	room.members[0] = &currentPVPMember{session: session, characterID: id, level: 90}
	session.pvpRoom = room
	return session, room, conn
}

func privateActivityPVPFatigue030Send(s *Service, session *gameSession, upper bool, difficulty byte) error {
	body := []byte{1, difficulty}
	if !upper {
		return s.handleGameCommand(session, 1, 1271, body)
	}
	packet, err := dnfproto.BuildChannelPacket(1271, body, 0, 1)
	if err != nil {
		return err
	}
	return s.handleGameUpper(session, packet)
}

func TestPrivateActivityPVPFatigue030NativePaymentReplay(t *testing.T) {
	for _, upper := range []bool{false, true} {
		t.Run(strconv.FormatBool(upper), func(t *testing.T) {
			s, g := privateActivityPVPFatigue030Fixture(t, 30)
			session, room, conn := privateActivityPVPFatigue030Session(29, 1)
			other := privateActivityPVPFatigue030Character(t, g, "30")
			conn.failAt = 1
			if err := privateActivityPVPFatigue030Send(s, session, upper, 1); err == nil {
				t.Fatal("missing injected socket failure after payment")
			}
			paid := privateActivityPVPFatigue030Character(t, g, "29")
			if paid.Stats["fatigue"] != 126 || paid.Stats["private_activity_v1_progress_6"] != 100 || paid.Stats["private_activity_v1_points"] != 20 || paid.Stats["private_activity_v1_done"] != 1<<5 || !room.arcade.paid {
				t.Fatalf("payment/activity not committed together: fatigue=%d progress=%d points=%d paid=%v", paid.Stats["fatigue"], paid.Stats["private_activity_v1_progress_6"], paid.Stats["private_activity_v1_points"], room.arcade.paid)
			}
			// A failing activity hook proves replay never tries to debit or score.
			privateActivityCatalogs.Store(s.initialEquipmentArchive, privateActivityCatalogResult{err: activeness.ErrCatalog})
			for retry := 0; retry < 2; retry++ {
				conn.write.Reset()
				if err := privateActivityPVPFatigue030Send(s, session, upper, 1); err != nil {
					t.Fatal("paid retry called activity hook", err)
				}
				state, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
				if state.Header.MsgID != 36 || state.Header.Classification != 0 || ack.Header.MsgID != 1271 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
					t.Fatal("retry did not publish fatigue state then native successful ACK")
				}
				if !reflect.DeepEqual(paid, privateActivityPVPFatigue030Character(t, g, "29")) {
					t.Fatal("duplicate payment changed fatigue/activity")
				}
			}
			if !reflect.DeepEqual(other, privateActivityPVPFatigue030Character(t, g, "30")) {
				t.Fatal("payment changed the other character")
			}
		})
	}
}

func TestPrivateActivityPVPFatigue030CharacterIsolationAndSaturation(t *testing.T) {
	s, g := privateActivityPVPFatigue030Fixture(t, 30)
	for _, id := range []uint16{29, 30, 29} {
		session, _, _ := privateActivityPVPFatigue030Session(id, 1)
		if err := privateActivityPVPFatigue030Send(s, session, true, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []struct {
		id                        string
		fatigue, progress, points int64
	}{{"29", 96, 100, 20}, {"30", 126, 41, 0}} {
		got := privateActivityPVPFatigue030Character(t, g, want.id)
		if got.Stats["fatigue"] != want.fatigue || got.Stats["private_activity_v1_progress_6"] != want.progress || got.Stats["private_activity_v1_points"] != want.points || got.Stats["private_activity_v1_claimed"] != 0 {
			t.Fatalf("character %s: fatigue=%d progress=%d points=%d", want.id, got.Stats["fatigue"], got.Stats["private_activity_v1_progress_6"], got.Stats["private_activity_v1_points"])
		}
	}
}

func TestPrivateActivityPVPFatigue030ZeroInsufficientAndCallbackRollback(t *testing.T) {
	for _, mode := range []string{"zero", "insufficient", "callback_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, g := privateActivityPVPFatigue030Fixture(t, 30)
			session, room, conn := privateActivityPVPFatigue030Session(29, 1)
			if mode == "zero" {
				s.pvpFatigueRules.ArcadeDifficulty[0] = 0
			}
			if mode == "insufficient" {
				c := privateActivityPVPFatigue030Character(t, g, "29")
				c.Stats["fatigue"], c.Stats["fatigue_used"], c.Stats["fatigue_display_update"] = 29, 127, 127
				if err := g.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			before := privateActivityPVPFatigue030Character(t, g, "29")
			other := privateActivityPVPFatigue030Character(t, g, "30")
			workingCatalog, err := s.privateActivityCatalog()
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected private activity catalog failure")
			privateActivityCatalogs.Store(s.initialEquipmentArchive, privateActivityCatalogResult{err: injected})
			for retry := 0; retry < 2; retry++ {
				conn.write.Reset()
				err := privateActivityPVPFatigue030Send(s, session, true, 1)
				if mode == "callback_failure" {
					if !errors.Is(err, injected) || room.arcade.paid || room.arcade.usageAttempted || conn.write.Len() != 0 {
						t.Fatal("callback failure committed payment/receipt/ACK", err)
					}
				} else if err != nil || room.arcade.paid != (mode == "zero") || !room.arcade.usageAttempted {
					t.Fatal("wrong zero/insufficient native result", err)
				}
				if !reflect.DeepEqual(before, privateActivityPVPFatigue030Character(t, g, "29")) || !reflect.DeepEqual(other, privateActivityPVPFatigue030Character(t, g, "30")) {
					t.Fatal("failed/zero request changed fatigue/activity or another character")
				}
			}
			if mode == "callback_failure" {
				// Restore a working catalog and retry the same run after rollback.
				privateActivityCatalogs.Store(s.initialEquipmentArchive, privateActivityCatalogResult{catalog: workingCatalog})
				if err := privateActivityPVPFatigue030Send(s, session, true, 1); err != nil {
					t.Fatal(err)
				}
				got := privateActivityPVPFatigue030Character(t, g, "29")
				if got.Stats["fatigue"] != 126 || got.Stats["private_activity_v1_progress_6"] != 100 || got.Stats["private_activity_v1_points"] != 20 {
					t.Fatal("retry after rollback failed to debit and score once")
				}
			}
		})
	}
}

func TestPrivateActivityPVPFatigue030RealPVFZeroCost(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the published PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := loadCurrentPVPFatigueRulesFromSource(archive)
	if err != nil {
		t.Fatal(err)
	}
	s, g := privateActivityPVPFatigue030Fixture(t, 0)
	s.initialEquipmentArchive, s.pvpFatigueRules = archive, rules
	t.Cleanup(func() { privateActivityCatalogs.Delete(archive) })
	catalog, err := s.privateActivityCatalog()
	if err != nil || catalog == nil || catalog.Tasks[5].Target != 100 || catalog.Tasks[5].Points != 20 {
		t.Fatal("real task6 catalog", err)
	}
	for difficulty := byte(1); difficulty <= 3; difficulty++ {
		t.Run(strconv.Itoa(int(difficulty)), func(t *testing.T) {
			cost, found := rules.arcadeFatigue(difficulty - 1)
			if !found || cost != 0 {
				t.Fatalf("012 difficulty %d actual cost=%d found=%v", difficulty, cost, found)
			}
			session, room, _ := privateActivityPVPFatigue030Session(29, difficulty)
			before := privateActivityPVPFatigue030Character(t, g, "29")
			for retry := 0; retry < 2; retry++ {
				if err := privateActivityPVPFatigue030Send(s, session, true, difficulty); err != nil {
					t.Fatal(err)
				}
			}
			if !room.arcade.paid || !reflect.DeepEqual(before, privateActivityPVPFatigue030Character(t, g, "29")) {
				t.Fatal("real zero-cost arcade altered fatigue or activity")
			}
		})
	}
}
