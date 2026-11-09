package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type weddingChurchLoadingFixture struct {
	s             *Service
	first, second *gameSession
	repos         r.Group
	now           time.Time
	at            int64
}

func newWeddingChurchLoadingFixture(t *testing.T) *weddingChurchLoadingFixture {
	t.Helper()
	s, first, second, _ := weddingRoomLeaseFixture(t)
	t.Cleanup(s.stopCurrentWeddingCeremonyTimers)
	t.Setenv(townDisplayOnlyEnv, "on")
	table, err := town.Load(context.Background(), townMoveTestSource{
		"town/town.lst":    "39 `church.twn` 38 `outside.twn`",
		"town/church.twn":  "[area]\n0 `outside.map` `[normal]`\n[/area]\n[area]\n8 `Church/Church.map` `[normal]`\n[/area]",
		"town/outside.twn": "[area]\n0 `outside.map` `[normal]`\n[/area]\n[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]",
	}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	// The town fixture replaces Character with a transport fake; use one real
	// memory aggregate group so both character rows and inventories share UOWs.
	repos := memory.NewMemoryGroup()
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	f := &weddingChurchLoadingFixture{s: s, first: first, second: second, repos: repos, now: time.Unix(1793534400, 0)}
	f.at = f.now.Unix() + 300
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return f.now }}
	s.wedding.rules.ForceMove = [2]wedding.CeremonyPosition{{X: 670, Y: 330}, {X: 685, Y: 360}}
	s.wedding.rules.Ceremonies = map[byte]wedding.Ceremony{0: {Kind: 0}}
	s.wedding.rules.CeremonyGifts = map[byte]wedding.Gift{0: {ItemID: 10002629, Count: 1}}
	s.wedding.rules.CeremonySeconds = 60
	source := cargoQuerySource()
	source["stackable/stackable.lst"] += "\n10002629 `weddinggift.stk`\n"
	source["stackable/weddinggift.stk"] = "[stackable type]\n`[booster]` 0\n[stack limit]\n1\n"
	source["map/church/church.map"] = "[virtual movable area]\n2 233 642 198 650 287 68 89\n"
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	// These tests exercise scene loading and gift placement. A persisted skill
	// ledger avoids unrelated initial-PVF backfilling; no skill catalog is needed.
	s.skillCatalog = nil
	s.initialSPTable = map[int]int{90: 20}
	pair := weddingCeremonyRecords()
	for index, c := range []r.CharacterRecord{pair.Actor, pair.Partner} {
		p := []*gameSession{first, second}[index]
		p.connID = "wedding-church-" + c.CharacterID
		p.connectionTownActorOwnerChannel, p.townActorOwnerChannel = byte(p.channel.ID), byte(p.channel.ID)
		p.townSceneReadyCharacterID = p.selectedCharacterID
		p.initialTownRouteCharacterID, p.initialTownRouteStage = p.selectedCharacterID, currentInitialTownRoutePlayerStateSent
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 39, AreaID: 0, PositionValid: true}
		p.postStartMapPlayerStateSent = false
		c.Stats["wedding_v1_ceremony_at"], c.Stats["wedding_v1_ceremony_channel"] = f.at, int64(p.channel.ID)
		c.Stats["town_id"], c.Stats["area_id"], c.Stats["area_state"] = 39, 0, 3
		c.Stats["pos_x"], c.Stats["pos_y"] = 670, 330
		if err := r.SaveCharacterFields(context.Background(), repos.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: c.CharacterID, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Equipment.Save(context.Background(), r.EquipmentRecord{CharacterID: c.CharacterID, Entries: map[string]r.EquipmentEntry{}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Skill.Save(context.Background(), r.SkillRecord{CharacterID: c.CharacterID, Skills: map[int64]r.SkillState{46: {Level: 1, Enabled: true}}, Layouts: map[int]r.SkillLayout{currentSkillInfoTreeIndex: {0: 46}}, Points: r.SkillPointState{TotalSP: 20, RemainingSP: 20, SyncedLevel: 90}}); err != nil {
			t.Fatal(err)
		}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, AccountID: c.AccountID, TownID: 39, AreaID: 0, Session: p})
		p.conn.(*bufferConn).write.Reset()
	}
	return f
}

func (f *weddingChurchLoadingFixture) enter(t *testing.T, p *gameSession) {
	t.Helper()
	partner := uint32(29)
	if p == f.first {
		partner = 30
	}
	body := make([]byte, 22)
	binary.LittleEndian.PutUint32(body[13:], partner)
	binary.LittleEndian.PutUint32(body[17:], 8)
	if err := f.s.handleCurrentWeddingCeremonyEntry(p, body); err != nil {
		t.Fatal(err)
	}
	if ack := guildReply(t, p, 1039); !bytes.Equal(ack, weddingResultACK(0)) {
		t.Fatalf("entry ACK %x", ack)
	}
	presence, found := f.s.onlinePlayers.PlayerForCharacter(p.selectedCharacterID)
	if !found || presence.Session != p || presence.TownID != 39 || presence.AreaID != 8 || presence.Instance != weddingPairInstance(29, 30) {
		t.Fatalf("real entry failed to commit Church presence: %+v", presence)
	}
	if p.initialTownRouteCharacterID != 0 || p.initialTownRouteStage != currentInitialTownRouteIdle || p.finishLoadingGateSnapshot() != (finishLoadingGates{}) {
		t.Fatal("entry did not reproduce the retired login route and empty new-scene gates")
	}
	if ready, source := f.s.currentSceneActorReadyForState(p); ready {
		t.Fatalf("fixture accidentally retained generic actor readiness: %s", source)
	}
	p.conn.(*bufferConn).write.Reset()
}

type weddingChurchLoadingFrame struct {
	class byte
	op    uint16
	body  []byte
}

func weddingChurchLoadingFrames(t *testing.T, p *gameSession) []weddingChurchLoadingFrame {
	t.Helper()
	var frames []weddingChurchLoadingFrame
	for wire := append([]byte(nil), p.conn.(*bufferConn).write.Bytes()...); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		frames = append(frames, weddingChurchLoadingFrame{packet.Header.Classification, packet.Header.MsgID, append([]byte(nil), packet.Body...)})
		wire = rest
	}
	p.conn.(*bufferConn).write.Reset()
	return frames
}

func weddingChurchLoadingFinish(t *testing.T, s *Service, p *gameSession) []weddingChurchLoadingFrame {
	t.Helper()
	if err := s.handleGameCommand(p, byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeFinishLoading), make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	return weddingChurchLoadingFrames(t, p)
}

func weddingChurchLoadingRows(t *testing.T, logs *bytes.Buffer, event string) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var rows []map[string]any
	for {
		var row map[string]any
		if err := decoder.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if row["event"] != event {
			continue
		}
		if row["level"] != "INFO" {
			t.Fatalf("non-INFO diagnostic %v", row)
		}
		for _, key := range []string{"account", "account_id", "name", "nonce", "body", "error"} {
			if _, present := row[key]; present {
				t.Fatalf("private diagnostic field %s", key)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func TestWeddingChurchLoadingRealEntryFinishAndTimer(t *testing.T) {
	for _, stale := range []string{"fresh_login", "completed_return_ack_only", "completed_7142_omit_activation"} {
		t.Run(stale, func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			logs := captureWeddingDiagnostics(f.s)
			for _, p := range []*gameSession{f.first, f.second} {
				if stale != "fresh_login" {
					p.returnTownFinishLoadingAckOnly = true
				}
				if stale == "completed_7142_omit_activation" {
					p.townPostTransition.generation = 17
					p.townPostTransition.characterID = p.selectedCharacterID
					p.townPostTransition.ownerChannel = currentConnectionTownActorOwnerContext(p)
					p.townPostTransition.stage = currentTownPostTransitionComplete
					p.townPostTransition.omitSceneActivation = true
				}
				f.enter(t, p)
				frames := weddingChurchLoadingFinish(t, f.s, p)
				stateAt, activationAt, placementAt := -1, -1, -1
				for index, frame := range frames {
					if frame.class == 0 && frame.op == 37 && len(frame.body) == currentFinishLoadingCharacterStateBodySize {
						stateAt = index
					}
					if frame.class == 0 && frame.op == 30 {
						activationAt = index
					}
					if frame.class == 0 && frame.op == 120 {
						placementAt = index
					}
				}
				if stateAt < 0 || activationAt <= stateAt || placementAt <= activationAt {
					t.Fatalf("missing real ordered state/activation/post tail: %+v", frames)
				}
				if p.finishLoadingGateSnapshot() != (finishLoadingGates{State: true, Completion: true, PostFinish: true}) || p.returnTownFinishLoadingAckOnly {
					t.Fatal("real Church tail did not complete gates/retire stale ACK-only policy")
				}
				if p.initialTownRouteCharacterID != 0 || p.initialTownRouteStage != currentInitialTownRouteIdle {
					t.Fatal("repair forged the retired login route")
				}
				for _, frame := range weddingChurchLoadingFinish(t, f.s, p) {
					if frame.class == 0 && (frame.op == 30 || frame.op == 37) {
						t.Fatalf("duplicate finish replayed state/activation: %+v", frame)
					}
				}
			}
			a, _ := f.s.boundGameSessionCharacterSnapshot(f.first)
			b, _ := f.s.boundGameSessionCharacterSnapshot(f.second)
			route := wedding.CeremonyRoute{Channel: byte(f.first.channel.ID), Area: 8}
			if !f.s.currentWeddingChurchPresence(a, b, route) {
				t.Fatal("real native finish handlers did not establish timer admission")
			}
			key, _ := r.WeddingPairScope([]string{"29", "30"})
			ceremony := f.s.wedding.ceremonies[key]
			if ceremony == nil || ceremony.timer == nil {
				t.Fatal("real entry failed to schedule the reservation")
			}
			ceremony.timer.Stop()
			f.now = time.Unix(f.at, 0)
			f.s.runCurrentWeddingCeremony(key, ceremony)
			for _, p := range []*gameSession{f.first, f.second} {
				id := strconv.Itoa(int(p.selectedCharacterID))
				c, _, err := f.repos.Character.Load(context.Background(), id)
				state, stateErr := wedding.Read(c)
				if err != nil || stateErr != nil || state.Phase != wedding.InCeremony || state.CeremonyReadyAt != f.at {
					t.Fatalf("due timer did not commit %s: %+v %v/%v", id, state, err, stateErr)
				}
				bag, _, _ := f.repos.Inventory.Load(context.Background(), id)
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 0)
				foundBegin := false
				for _, frame := range weddingChurchLoadingFrames(t, p) {
					foundBegin = foundBegin || frame.class == 0 && frame.op == 1007 && len(frame.body) == 68
				}
				if !foundBegin {
					t.Fatal("committed wedding did not send native ceremony begin")
				}
			}
			rows := weddingChurchLoadingRows(t, logs, "game-wedding-church-finish-loading")
			if len(rows) != 2 {
				t.Fatalf("loading diagnostics repeated or missing: %v", rows)
			}
			for _, row := range rows {
				if row["outcome"] != "delivered" || row["completion_sent"] != true {
					t.Fatalf("incomplete loading diagnostic: %v", row)
				}
			}
			progress := weddingChurchLoadingRows(t, logs, "game-wedding-ceremony-progress")
			if len(progress) != 1 || progress[0]["outcome"] != "committed" || progress[0]["first_begin_sent"] != true || progress[0]["second_begin_sent"] != true {
				t.Fatalf("commit diagnostic %v", progress)
			}
			ceremony.timer.Stop()
			f.now = time.Unix(f.at+59, 0)
			f.s.runCurrentWeddingCeremony(key, ceremony)
			c, _, _ := f.repos.Character.Load(context.Background(), "29")
			if state, _ := wedding.Read(c); state.Phase != wedding.InCeremony {
				t.Fatal("completed before server end interval")
			}
			// Native movie cleanup may leave the Church before the server end
			// event. The committed admission, not another live scene check, owns
			// both rewards; disconnect/ordinary departure cannot lose them.
			for _, id := range []string{"29", "30"} {
				c, _, _ := f.repos.Character.Load(context.Background(), id)
				c.Stats["town_id"], c.Stats["area_id"] = 39, 0
				if err := r.SaveCharacterFields(context.Background(), f.repos.Character, c, r.CharacterFieldStats); err != nil {
					t.Fatal(err)
				}
			}
			ceremony.timer.Stop()
			f.now = time.Unix(f.at+60, 0)
			f.s.runCurrentWeddingCeremony(key, ceremony)
			for _, id := range []string{"29", "30"} {
				c, _, _ := f.repos.Character.Load(context.Background(), id)
				state, e := wedding.Read(c)
				if e != nil || state.Phase != wedding.Married {
					t.Fatalf("end did not commit %s %+v %v", id, state, e)
				}
				bag, _, _ := f.repos.Inventory.Load(context.Background(), id)
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 1)
			}
			f.s.runCurrentWeddingCeremony(key, ceremony)
			for _, id := range []string{"29", "30"} {
				bag, _, _ := f.repos.Inventory.Load(context.Background(), id)
				assertCurrentNPCShopInventoryAmount(t, bag, 10002629, 1)
			}
		})
	}
}

func TestWeddingChurchLoadingPreservesRealReturnAndOtherOwnerPaths(t *testing.T) {
	for _, scenario := range []string{"pending_return", "selector_return", "dungeon", "single", "married"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			f.enter(t, f.first)
			p := f.first
			p.returnTownFinishLoadingAckOnly = true
			switch scenario {
			case "pending_return":
				p.confirmedDungeonReturnStatePending = true
			case "selector_return":
				p.selectorReturnPresence = &currentSelectorReturnPresence{}
			case "dungeon":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "single", "married":
				c, _, _ := f.repos.Character.Load(context.Background(), "29")
				c.Stats["wedding_v1_phase"] = int64(wedding.Married)
				if scenario == "single" {
					c.Stats["wedding_v1_phase"], c.Stats["wedding_v1_partner"] = 0, 0
				}
				if err := r.SaveCharacterFields(context.Background(), f.repos.Character, c, r.CharacterFieldWedding); err != nil {
					t.Fatal(err)
				}
			}
			if handled, err := f.s.handleCurrentWeddingChurchFinishLoading(p); handled || err != nil {
				t.Fatalf("intercepted another owner: handled=%t err=%v", handled, err)
			}
			if !p.returnTownFinishLoadingAckOnly || p.finishLoadingGateSnapshot() != (finishLoadingGates{}) || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("failed proof changed an existing return or wrote scene packets")
			}
			if scenario == "pending_return" && !p.confirmedDungeonReturnStatePending {
				t.Fatal("pending return was consumed")
			}
			if scenario == "selector_return" && p.selectorReturnPresence == nil {
				t.Fatal("selector return was consumed")
			}
		})
	}
}

func TestWeddingChurchLoadingProofRejectsChangedSceneIdentityAndScope(t *testing.T) {
	for _, scenario := range []string{"presence_generation", "canonical_generation", "account", "channel", "instance", "town_scene"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			f.enter(t, f.first)
			f.s.wedding.mu.Lock()
			proof, reason := f.s.currentWeddingChurchLoadingProof(context.Background(), f.first)
			f.s.wedding.mu.Unlock()
			if reason != "owned_church_scene" {
				t.Fatalf("initial valid proof rejected: %s", reason)
			}
			p, _ := f.s.onlinePlayers.PlayerForCharacter(29)
			switch scenario {
			case "presence_generation":
				f.s.onlinePlayers.EnterArea(&p)
			case "canonical_generation":
				f.first.characterGeneration++
			case "account":
				f.first.accountID = "different-account"
			case "channel":
				f.first.channel.ID++
			case "instance":
				p.Instance = 0
				f.s.onlinePlayers.EnterArea(&p)
			case "town_scene":
				f.first.townPositionSnapshot.AreaID = 0
			}
			if f.s.currentWeddingChurchLoadingCurrent(proof) {
				t.Fatal("stale or mismatched scene proof accepted")
			}
		})
	}
}

func TestWeddingChurchLoadingWaitingAndExpiryRemainVisibleWithoutPacketLog(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	logs := captureWeddingDiagnostics(f.s)
	f.enter(t, f.first)
	f.enter(t, f.second)
	// A real retained scene owner, rather than absent dungeon loading gates,
	// blocks admission after an otherwise successful native town transition.
	f.first.confirmedDungeonReturnStatePending = true
	key, _ := r.WeddingPairScope([]string{"29", "30"})
	ceremony := f.s.wedding.ceremonies[key]
	ceremony.timer.Stop()
	f.now = time.Unix(f.at, 0)
	for range 2 {
		f.s.runCurrentWeddingCeremony(key, ceremony)
		if ceremony.timer == nil {
			t.Fatal("waiting reservation lost its expiration timer")
		}
		ceremony.timer.Stop()
	}
	rows := weddingChurchLoadingRows(t, logs, "game-wedding-ceremony-progress")
	if len(rows) != 1 || rows[0]["reason"] != "first_town_scene_owner" || rows[0]["outcome"] != "waiting" {
		t.Fatalf("waiting log was absent or not limited: %v", rows)
	}
	f.now = time.Unix(f.at+wedding.CeremonyAdmissionGraceSeconds+1, 0)
	f.s.runCurrentWeddingCeremony(key, ceremony)
	rows = weddingChurchLoadingRows(t, logs, "game-wedding-ceremony-progress")
	if len(rows) != 2 || rows[1]["outcome"] != "expired" {
		t.Fatalf("expiry diagnostic missing: %v", rows)
	}
	for _, id := range []string{"29", "30"} {
		c, _, _ := f.repos.Character.Load(context.Background(), id)
		state, err := wedding.Read(c)
		if err != nil || state.Phase != wedding.Engaged || state.CeremonyAt != 0 || state.CeremonyReadyAt != 0 {
			t.Fatalf("unready reservation unexpectedly married: %+v %v", state, err)
		}
	}
}

func TestWeddingChurchLoadingFailedRealStateWriteDoesNotCreateDeliveredGates(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	logs := captureWeddingDiagnostics(f.s)
	f.enter(t, f.first)
	// The native success ACK is write one; the actual 87B state is write two.
	f.first.conn = &weddingRoomWriteFailure{failOn: 2}
	if err := f.s.sendFinishLoadingStatus(f.first, make([]byte, 8)); err == nil {
		t.Fatal("failed state write was hidden")
	}
	if f.first.finishLoadingGateSnapshot() != (finishLoadingGates{}) {
		t.Fatal("failed real state packet manufactured a delivered gate")
	}
	rows := weddingChurchLoadingRows(t, logs, "game-wedding-church-finish-loading")
	if len(rows) != 1 || rows[0]["outcome"] != "deferred" || rows[0]["reason"] != "state_tail_failed" {
		t.Fatalf("state failure diagnostic missing: %v", rows)
	}
}
