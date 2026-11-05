package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestWeddingCurrentPVFPriestConsentIngress(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("matching PVF not supplied")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var priestTown, priestArea byte
	priestFound := false
findPriest:
	for _, location := range towns.Towns() {
		if location.ID < 0 || location.ID > 255 {
			continue
		}
		for _, area := range location.Areas {
			if area.ID < 0 || area.ID > 255 {
				continue
			}
			placement, err := worldmap.LoadTownPlacementMap(archive, area.MapPath)
			if err != nil {
				continue
			}
			for _, npc := range placement.NPCs {
				if npc.NPCID == 20013 {
					priestTown, priestArea, priestFound = byte(location.ID), byte(area.ID), true
					break findPriest
				}
			}
		}
	}
	if !priestFound {
		t.Fatal("runtime PVF has no town priest placement")
	}
	rules, err := wedding.Load(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T) (*Service, []*gameSession, r.Group) {
		t.Helper()
		s, p, repos := guildFoundationFixture(t)
		s.options.currentClientVersion = "official-24.9"
		s.initialEquipmentArchive = archive
		s.townCatalog = towns
		s.pvfItemCatalog = items
		s.wedding.rules = rules
		s.onlinePlayers = newOnlinePlayerManager()
		for i, peer := range p[:2] {
			peer.channel = channelcatalog.Channel{ID: 10}
			peer.connectionTownActorOwnerChannel, peer.townActorOwnerChannel = 10, 10
			peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: peer.selectedCharacterID, TownID: priestTown, AreaID: priestArea, PositionX: 600, PositionY: 250, PositionValid: true}
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: peer.selectedCharacterID, Session: peer, TownID: priestTown, AreaID: priestArea, PositionX: 600, PositionY: 250})
			c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(i+1))
			if i == 0 {
				c.Job = "0"
			} else {
				c.Job = "11"
			}
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]r.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Equipment.Save(context.Background(), r.EquipmentRecord{CharacterID: c.CharacterID, Entries: map[string]r.EquipmentEntry{}}); err != nil {
				t.Fatal(err)
			}
			identity, _ := s.boundGameSessionCharacterSnapshot(peer)
			if err := s.validateUpgradeNPCScene(identity, 20013); err != nil {
				t.Fatalf("priest fixture: %v", err)
			}
		}
		return s, p, repos
	}
	t.Run("single-profile-search-uses-receiver-token", func(t *testing.T) {
		s, peers, _ := newFixture(t)
		body := make([]byte, 17)
		binary.LittleEndian.PutUint32(body[13:], 2)
		sendCargoWire(t, s, peers[0], false, 1035, 1, body)
		ops, profiles := cargoReplies(t, peers[0])
		if len(ops) != 1 || ops[0] != 1035 || len(profiles[0]) != 98 || profiles[0][0] != 1 ||
			binary.LittleEndian.Uint32(profiles[0][5:]) != 0 || profiles[0][43] != 90 || profiles[0][44] != 11 {
			t.Fatalf("single search did not preserve receiver Info1000 token %v %x", ops, profiles)
		}
	})
	requestBody := make([]byte, 20)
	binary.LittleEndian.PutUint32(requestBody[13:], 2)
	requestBody[17] = 1
	request := func(t *testing.T, s *Service, p []*gameSession, upper bool, seq uint16, success bool) {
		t.Helper()
		sendCargoWire(t, s, p[0], upper, 1032, seq, requestBody)
		ops, bodies := cargoReplies(t, p[0])
		if len(ops) != 1 || ops[0] != 1032 || len(bodies[0]) != 12 || (binary.LittleEndian.Uint32(bodies[0][1:]) == 0) != success {
			t.Fatalf("request ACK success=%t: %v %x", success, ops, bodies)
		}
		ops, bodies = cargoReplies(t, p[1])
		if !success {
			if len(ops) != 0 {
				t.Fatalf("rejected request opened another dialog: %v %x", ops, bodies)
			}
			return
		}
		if len(ops) != 1 || ops[0] != 997 || len(bodies[0]) != 11 || binary.LittleEndian.Uint32(bodies[0][4:]) != 1 {
			t.Fatalf("partner dialog %v %x", ops, bodies)
		}
	}
	response := func(target uint32, operation, answer byte) []byte {
		body := make([]byte, 21)
		binary.LittleEndian.PutUint32(body[13:], target)
		body[17], body[20] = operation, answer
		return body
	}
	assertResponse := func(t *testing.T, p []*gameSession, answer byte) {
		t.Helper()
		ops, bodies := cargoReplies(t, p[1])
		if len(ops) == 0 || ops[0] != 1033 || len(bodies[0]) != 13 || binary.LittleEndian.Uint32(bodies[0][1:]) != 0 || binary.LittleEndian.Uint32(bodies[0][5:]) != 1 || bodies[0][12] != answer {
			t.Fatalf("response ACK answer=%d: %v %x", answer, ops, bodies)
		}
		ops, bodies = cargoReplies(t, p[0])
		if len(ops) == 0 || ops[0] != 998 || len(bodies[0]) != 12 || binary.LittleEndian.Uint32(bodies[0]) != 0 || binary.LittleEndian.Uint32(bodies[0][4:]) != 2 || bodies[0][11] != answer {
			t.Fatalf("response notice answer=%d: %v %x", answer, ops, bodies)
		}
	}
	assertSingle := func(t *testing.T, repos r.Group) {
		t.Helper()
		for _, id := range []string{"1", "2"} {
			c, _, err := repos.Character.Load(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			state, err := wedding.Read(c)
			if err != nil || state.Phase != wedding.Single {
				t.Fatalf("non-consent changed character %s: %+v %v", id, state, err)
			}
		}
	}
	t.Run("reservation-congratulations-opens-after-deadline-projection", func(t *testing.T) {
		s, peers, repos := newFixture(t)
		t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
		owner, err := s.currentWeddingOwner()
		if err != nil {
			t.Fatal(err)
		}
		a, _ := s.boundGameSessionCharacterSnapshot(peers[0])
		b, _ := s.boundGameSessionCharacterSnapshot(peers[1])
		if _, err = owner.Engage(context.Background(), s.weddingPairRequest(a, b)); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"1", "2"} {
			c, _, _ := repos.Character.Load(context.Background(), id)
			c.Stats["gold"] = rules.Ceremonies[0].Gold + 123
			if err = repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			q := r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{}}
			for _, qid := range rules.QuestIDs {
				q.States[qid] = r.QuestState{Status: "complete"}
			}
			if err = repos.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
		}
		request := append([]byte(nil), requestBody...)
		request[17] = 2
		if err = s.handleCurrentWeddingRequest(peers[0], request); err != nil {
			t.Fatal(err)
		}
		cargoReplies(t, peers[0])
		cargoReplies(t, peers[1])
		if err = s.handleCurrentWeddingResponse(peers[1], response(1, 2, 0)); err != nil {
			t.Fatal(err)
		}
		var token uint32
		for i, p := range peers[:2] {
			ops, bodies := cargoReplies(t, p)
			info, profile, deadline, success, successes := -1, -1, -1, -1, 0
			for n, op := range ops {
				switch op {
				case 1000:
					info = n
					token = binary.LittleEndian.Uint32(bodies[n])
				case 1035:
					profile = n
					if token == 0 || binary.LittleEndian.Uint32(bodies[n][5:]) != token {
						t.Fatal("profile token mismatch")
					}
				case 1004:
					deadline = n
					if binary.LittleEndian.Uint32(bodies[n]) != token {
						t.Fatal("deadline token mismatch")
					}
				case 1033:
					if len(bodies[n]) == 13 && bodies[n][0] == 1 && binary.LittleEndian.Uint32(bodies[n][1:]) == 0 && bodies[n][12] == 0 {
						successes++
					}
					if i == 1 {
						success = n
					}
				case 998:
					if len(bodies[n]) == 12 && binary.LittleEndian.Uint32(bodies[n]) == 0 && bodies[n][11] == 0 {
						successes++
					}
					if i == 0 {
						success = n
					}
				}
			}
			if info < 0 || profile <= info || deadline <= profile || success <= deadline || successes != 1 {
				t.Fatalf("congratulations opened before its clearing projection: %v", ops)
			}
		}
		c, _, _ := repos.Character.Load(context.Background(), "1")
		if c.Stats["gold"] != 123 {
			t.Fatal("reservation charged incorrectly")
		}
	})
	t.Run("reservation-server-timeout-notifies-both-and-late-timeout-preserves-replacement", func(t *testing.T) {
		s, peers, repos := newFixture(t)
		t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
		now := time.Unix(1793631600, 0)
		s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
		owner, _ := s.currentWeddingOwner()
		a, _ := s.boundGameSessionCharacterSnapshot(peers[0])
		b, _ := s.boundGameSessionCharacterSnapshot(peers[1])
		if _, e := owner.Engage(context.Background(), s.weddingPairRequest(a, b)); e != nil {
			t.Fatal(e)
		}
		q := append([]byte(nil), requestBody...)
		q[17] = 2
		if e := s.handleCurrentWeddingRequest(peers[0], q); e != nil {
			t.Fatal(e)
		}
		cargoReplies(t, peers[0])
		cargoReplies(t, peers[1])
		inv := s.wedding.invitations[2]
		if inv == nil || inv.timer == nil {
			t.Fatal("reservation lacks server-owned timeout")
		}
		if inv.expires.Sub(now) != 6*time.Second {
			t.Fatal("reservation deadline differs from the native 6000ms recipient window")
		}
		before, _, _ := repos.Character.Load(context.Background(), "1")
		now = now.Add(5999 * time.Millisecond)
		s.wedding.mu.Lock()
		early := s.expireCurrentWeddingReservationInvitationLocked(inv)
		s.wedding.mu.Unlock()
		if early != nil || s.wedding.invitations[2] != inv {
			t.Fatal("reservation expired before the recipient's reply window ended")
		}
		for _, p := range peers[:2] {
			if ops, _ := cargoReplies(t, p); len(ops) != 0 {
				t.Fatal("premature native timeout notification")
			}
		}
		now = now.Add(time.Millisecond)
		s.wedding.mu.Lock()
		e := s.expireCurrentWeddingReservationInvitationLocked(inv)
		s.wedding.mu.Unlock()
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range peers[:2] {
			ops, bodies := cargoReplies(t, p)
			if len(ops) != 1 || ops[0] != 998 || len(bodies[0]) != 12 || bodies[0][8] != 2 || bodies[0][11] != 2 {
				t.Fatalf("native timeout notification %v %x", ops, bodies)
			}
		}
		if e := s.handleCurrentWeddingResponse(peers[1], response(0, 2, 2)); e != nil {
			t.Fatal(e)
		}
		cargoReplies(t, peers[1])
		if ops, _ := cargoReplies(t, peers[0]); len(ops) != 0 {
			t.Fatal("late timeout duplicated initiator prompt")
		}
		if e := s.handleCurrentWeddingRequest(peers[0], q); e != nil {
			t.Fatal(e)
		}
		ops, bodies := cargoReplies(t, peers[0])
		if len(ops) != 2 || ops[0] != 2 || bodies[0][0] != 8 || ops[1] != 1032 || binary.LittleEndian.Uint32(bodies[1][1:]) != 0 {
			t.Fatalf("reapply became character-not-found %v %x", ops, bodies)
		}
		cargoReplies(t, peers[1])
		replacement := s.wedding.invitations[2]
		if e := s.handleCurrentWeddingResponse(peers[1], response(0, 2, 2)); e != nil {
			t.Fatal(e)
		}
		if replacement == nil || s.wedding.invitations[2] != replacement {
			t.Fatal("old timeout consumed a new invitation")
		}
		after, _, _ := repos.Character.Load(context.Background(), "1")
		if before.Stats["gold"] != after.Stats["gold"] || before.Stats["wedding_v1_wedding_day"] != after.Stats["wedding_v1_wedding_day"] || after.Stats["wedding_v1_phase"] != int64(wedding.Engaged) {
			t.Fatal("timeout spent assets or reserved a ceremony")
		}
		for _, p := range peers[:2] {
			cargoReplies(t, p)
		}
		s.stopCurrentWeddingCeremonyTimers()
		if len(s.wedding.invitations) != 0 || len(s.wedding.reservationTimeouts) != 0 {
			t.Fatal("shutdown retained reservation dialogs")
		}
		s.wedding.mu.Lock()
		e = s.expireCurrentWeddingReservationInvitationLocked(replacement)
		s.wedding.mu.Unlock()
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range peers[:2] {
			if ops, _ := cargoReplies(t, p); len(ops) != 0 {
				t.Fatal("stopped callback published into retired invitation")
			}
		}
	})
	t.Run("reservation-daily-limit-opens-native-popup-before-inviting", func(t *testing.T) {
		s, peers, repos := newFixture(t)
		t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
		owner, _ := s.currentWeddingOwner()
		a, _ := s.boundGameSessionCharacterSnapshot(peers[0])
		b, _ := s.boundGameSessionCharacterSnapshot(peers[1])
		if _, e := owner.Engage(context.Background(), s.weddingPairRequest(a, b)); e != nil {
			t.Fatal(e)
		}
		c, _, _ := repos.Character.Load(context.Background(), "1")
		c.Stats["wedding_v1_wedding_day"] = currentWeddingDay(s.gameplayNow())
		if e := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldWedding); e != nil {
			t.Fatal(e)
		}
		q := append([]byte(nil), requestBody...)
		q[17], q[18] = 2, 2
		if e := s.handleCurrentWeddingRequest(peers[0], q); e != nil {
			t.Fatal(e)
		}
		ops, bodies := cargoReplies(t, peers[0])
		if len(ops) != 2 || ops[0] != 1032 || bodies[0][0] != 0 || ops[1] != 998 || fmt.Sprintf("%x", bodies[1]) != "f5ffffff0200000002020000" {
			t.Fatalf("daily limit did not use the verified native popup result %v %x", ops, bodies)
		}
		if other, _ := cargoReplies(t, peers[1]); len(other) != 0 || len(s.wedding.invitations) != 0 {
			t.Fatal("unpayable reservation opened recipient dialog")
		}
		after, _, e := repos.Character.Load(context.Background(), "1")
		if e != nil || after.Stats["gold"] != c.Stats["gold"] || after.Stats["wedding_v1_phase"] != c.Stats["wedding_v1_phase"] || after.Stats["wedding_v1_wedding_day"] != c.Stats["wedding_v1_wedding_day"] {
			t.Fatal("daily limit popup changed assets or relationship")
		}
	})
	for _, tc := range []struct {
		name string
		kind byte
		code int32
	}{{"gold", 0, -16}, {"quest", 0, -9}, {"ticket", 2, -14}, {"scene", 0, -1}} {
		t.Run("reservation-consent-failure-notifies-both/"+tc.name, func(t *testing.T) {
			s, peers, repos := newFixture(t)
			t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
			ctx := context.Background()
			owner, e := s.currentWeddingOwner()
			if e != nil {
				t.Fatal(e)
			}
			a, _ := s.boundGameSessionCharacterSnapshot(peers[0])
			b, _ := s.boundGameSessionCharacterSnapshot(peers[1])
			if _, e = owner.Engage(ctx, s.weddingPairRequest(a, b)); e != nil {
				t.Fatal(e)
			}
			for _, id := range []string{"1", "2"} {
				c, _, _ := repos.Character.Load(ctx, id)
				c.Stats["gold"] = rules.Ceremonies[tc.kind].Gold + 100
				if id == "1" && tc.name == "gold" {
					c.Stats["gold"] = 0
				}
				if e = repos.Character.Save(ctx, c); e != nil {
					t.Fatal(e)
				}
				q := r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{}}
				if tc.name != "quest" {
					for _, qid := range rules.QuestIDs {
						q.States[qid] = r.QuestState{Status: "complete"}
					}
				}
				if e = repos.Quest.Save(ctx, q); e != nil {
					t.Fatal(e)
				}
			}
			before := captureWeddingCancellationState(t, repos, peers[:2])
			q := append([]byte(nil), requestBody...)
			q[17], q[18] = 2, tc.kind
			if e = s.handleCurrentWeddingRequest(peers[0], q); e != nil {
				t.Fatal(e)
			}
			for _, p := range peers[:2] {
				cargoReplies(t, p)
			}
			if tc.name == "scene" {
				peers[0].townPositionSnapshot.PositionValid = false
			}
			if e = s.handleCurrentWeddingResponse(peers[1], response(1, 2, 0)); e != nil {
				t.Fatal(e)
			}
			for i, p := range peers[:2] {
				ops, bodies := cargoReplies(t, p)
				wantOp, offset := uint16(998), 0
				if i == 1 {
					wantOp, offset = 1033, 1
				}
				if len(ops) != 1 || ops[0] != wantOp || len(bodies[0]) != 12+offset || int32(binary.LittleEndian.Uint32(bodies[0][offset:])) != tc.code {
					t.Fatalf("party%d retained waiting UI or got wrong error: %v %x", i, ops, bodies)
				}
			}
			if len(s.wedding.invitations) != 0 || !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, peers[:2])) {
				t.Fatal("failed consent retained invitation or changed persisted assets")
			}
		})
	}
	for _, upper := range []bool{false, true} {
		for _, answer := range []byte{0, 1, 2} {
			t.Run(fmt.Sprintf("upper=%t/answer=%d", upper, answer), func(t *testing.T) {
				s, p, repos := newFixture(t)
				request(t, s, p, upper, 1, true)
				// Native accept names the inviter; reject/timeout callbacks send zero.
				target := uint32(0)
				if answer == 0 {
					target = 1
				}
				sendCargoWire(t, s, p[1], upper, 1033, 2, response(target, 1, answer))
				assertResponse(t, p, answer)
				a, _, _ := repos.Character.Load(context.Background(), "1")
				b, _, _ := repos.Character.Load(context.Background(), "2")
				if answer == 0 {
					x, y, err := wedding.ValidatePair(a, b)
					if err != nil || x.Phase != wedding.Engaged || y.Phase != wedding.Engaged {
						t.Fatalf("pair %+v %+v %v", x, y, err)
					}
				} else {
					assertSingle(t, repos)
					request(t, s, p, upper, 3, true)
				}
			})
		}
		for _, tc := range []struct {
			name      string
			target    uint32
			operation byte
			answer    byte
		}{
			{"zero-target-consent", 0, 1, 0},
			{"wrong-target-consent", 3, 1, 0},
			{"wrong-target-refusal", 3, 1, 1},
			{"wrong-target-timeout", 3, 1, 2},
			{"wrong-operation", 0, 2, 2},
		} {
			t.Run(fmt.Sprintf("upper=%t/pending/%s", upper, tc.name), func(t *testing.T) {
				s, p, repos := newFixture(t)
				request(t, s, p, upper, 1, true)
				sendCargoWire(t, s, p[1], upper, 1033, 2, response(tc.target, tc.operation, tc.answer))
				ops, bodies := cargoReplies(t, p[1])
				if len(ops) != 1 || ops[0] != 1033 || len(bodies[0]) != 13 || binary.LittleEndian.Uint32(bodies[0][1:]) == 0 {
					t.Fatalf("invalid response accepted: %v %x", ops, bodies)
				}
				ops, bodies = cargoReplies(t, p[0])
				if len(ops) != 0 {
					t.Fatalf("invalid response notified inviter: %v %x", ops, bodies)
				}
				assertSingle(t, repos)
				request(t, s, p, upper, 3, false)
				// A valid answer must still resolve the original pending dialog.
				sendCargoWire(t, s, p[1], upper, 1033, 4, response(0, 1, 1))
				assertResponse(t, p, 1)
				request(t, s, p, upper, 5, true)
			})
		}
		for _, answer := range []byte{0, 2} {
			t.Run(fmt.Sprintf("upper=%t/expired/answer=%d", upper, answer), func(t *testing.T) {
				s, p, repos := newFixture(t)
				now := time.Unix(1793631600, 0)
				s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
				request(t, s, p, upper, 1, true)
				now = now.Add(31 * time.Second)
				// Keep the expired dialog fenced until its late native reply drains.
				request(t, s, p, upper, 2, false)
				target := uint32(0)
				if answer == 0 {
					target = 1
				}
				sendCargoWire(t, s, p[1], upper, 1033, 3, response(target, 1, answer))
				assertResponse(t, p, 2)
				assertSingle(t, repos)
				request(t, s, p, upper, 4, true)
			})
		}
	}
}
