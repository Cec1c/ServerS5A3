package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Matched regression, originally red before the transactional COMMAND475 owner.
// This never loads a player database or writes the PVF.
func seekerImageCommunicationFixture(t *testing.T) (*platformpvf.Archive, *dnfquest.Catalog, string) {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matched QST-005 PVF")
	}
	f, err := os.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, hashErr := io.Copy(h, f)
	closeErr := f.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash PVF: %v / %v", hashErr, closeErr)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525" {
		t.Fatalf("unmatched QST-005 PVF: %s", got)
	}
	a, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return a, catalog, pvfPath
}

func seekerImageCommunicationDoc(t *testing.T, a *platformpvf.Archive, path string) *dnfpvf.Document {
	t.Helper()
	text, err := a.ReadText(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSeekerImageCommunicationRealPVFAnchors(t *testing.T) {
	a, catalog, _ := seekerImageCommunicationFixture(t)
	def, ok := catalog.Find(3734)
	if !ok || !strings.HasSuffix(strings.ToLower(def.Path), "slough/slough_epic_13.qst") || def.Type != "[meet npc]" || !reflect.DeepEqual(def.IntData, []int64{2000}) || def.LevelMin != 82 {
		t.Fatalf("quest3734=%+v found=%t", def, ok)
	}
	q := seekerImageCommunicationDoc(t, a, def.Path)
	for name, want := range map[string]int64{"npc index": 78, "complete npc index": 2000} {
		if got, ok := q.Int(name); !ok || got != want {
			t.Fatalf("quest %s=%d found=%t want=%d", name, got, ok, want)
		}
	}
	config := seekerImageCommunicationDoc(t, a, "etc/imagecommunication.etc")
	for name, want := range map[string][]int64{"charge": {10000}, "summon time": {20000}, "summon pos": {1274, 140, 0}, "npc index": {2000, 2001}, "require quest": {3734, 3741}} {
		var got []int64
		for _, section := range config.Sections {
			if section.Name == name {
				for _, token := range config.Tokens[section.Start:section.End] {
					if token.Kind == dnfpvf.TokenInt {
						got = append(got, token.Int)
					}
				}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("communication %s=%v want=%v", name, got, want)
		}
	}
	t.Logf("quest=%d %s type=%s target=%v; charge10000; NPC2000 requires3734; NPC2001 requires3741", def.ID, def.Path, def.Type, def.IntData)
}

func TestSeekerImageCommunication475RealPVF(t *testing.T) {
	a, catalog, pvfPath := seekerImageCommunicationFixture(t)
	s, session, repos, conn := newCarryGoldFixture(t, 20000, 0)
	s.options.pvfPath = pvfPath
	s.questCatalog = catalog
	session.connID = "qst005-offline"
	session.loginPacketSeen = true
	s.bindGameSessionCharacter(session, 17)
	session.townMu.Lock()
	setCurrentTownPositionSceneLocked(session, 17, 14, 1)
	session.townSceneReadyCharacterID = 17
	session.townMu.Unlock()
	ctx := context.Background()
	character, _, err := repos.Character.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	character.Level = 82
	character.Job = "2"
	character.Stats["exp"] = 0
	character.Stats["town_id"], character.Stats["area_id"] = 14, 1
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	for _, save := range []func() error{
		func() error {
			return repos.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "17", Points: dnfrepo.SkillPointState{SyncedLevel: 82}, Layouts: map[int]dnfrepo.SkillLayout{currentSkillInfoTreeIndex: {}}})
		},
		func() error {
			return repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "17", Slots: map[string]dnfrepo.ItemStack{}})
		},
		func() error { return repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "17"}) },
	} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	quests := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{
		3697: {Status: "completed"}, // PVF town14/area1 permission.
		3733: {Status: "completed"}, 3734: {Status: "active", ProgressValue: 1},
	}}
	if err := repos.Quest.Save(ctx, quests); err != nil {
		t.Fatal(err)
	}
	_, scene, err := s.currentImageCommunicationScene(ctx, 14, 1)
	if err != nil {
		t.Fatalf("real source scene: %v", err)
	}
	t.Logf("real source scene: %+v", scene)
	// The actual V124 sender has no NPC ID, fee or client balance in its body.
	if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketImageCommunicationEquipmentUse), nil); err != nil {
		t.Fatal(err)
	}
	after, _, err := repos.Character.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() == 0 {
		t.Fatalf("QST-005: valid bodyless COMMAND475 emitted no reply; gold=%d (before20000), want class1/op475 success+charge10000+remaining10000", after.Stats["gold"])
	}
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if packet.Header.Classification != 1 || packet.Header.MsgID != 475 || len(packet.Body) != 9 || packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[1:5]) != 10000 || binary.LittleEndian.Uint32(packet.Body[5:9]) != 10000 {
		t.Fatalf("COMMAND475 reply=%+v %x", packet.Header, packet.Body)
	}
	if after.Stats["gold"] != 10000 {
		t.Fatalf("gold after communication=%d want10000", after.Stats["gold"])
	}
	afterQuest, _, err := repos.Quest.Load(ctx, "17")
	if err != nil || !reflect.DeepEqual(afterQuest, quests) {
		t.Fatalf("475 must not itself finish the NPC quest: %+v err=%v", afterQuest, err)
	}
	// The existing op33/op34 owners, not 475, still own quest progress and
	// rewards. Use the actual quest and progression tables for this chain.
	conn.write.Reset()
	if err := s.handleGameCommand(session, 1, 33, []byte{33, 0, 0x96, 0x0e, 0, 0}); err != nil {
		t.Fatal(err)
	}
	afterQuest, _, err = repos.Quest.Load(ctx, "17")
	if err != nil || afterQuest.States[3734].Status != "active" || afterQuest.States[3734].ProgressValue != 0 {
		t.Fatalf("op33 progress=%+v err=%v", afterQuest, err)
	}
	tables, err := progression.Load(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	request := dnfquest.FinishQuestRequest{QuestID: 3734, RewardSelectIndex: ^uint16(0), Multiplier: 1, Reserved: dnfquest.CurrentFinishQuestObservedTailMarker}
	noItems := func(_ *dnfrepo.InventoryRecord, request dnfquest.FinishItemGrantRequest) ([]dnfquest.FinishCommittedItem, error) {
		return nil, fmt.Errorf("unexpected item reward in real3734: %+v", request)
	}
	if err := s.handleCurrentFinishQuestWithResources(session, request, repos, catalog, tables, noItems); err != nil {
		t.Fatal(err)
	}
	completed, _, err := repos.Quest.Load(ctx, "17")
	if err != nil || completed.States[3734].Status != "completed" {
		t.Fatalf("op34 state=%+v err=%v", completed.States[3734], err)
	}
	finishedCharacter, _, _ := repos.Character.Load(ctx, "17")
	for i := 0; i < 2; i++ {
		conn.write.Reset()
		if err := s.handleGameCommand(session, 1, 475, nil); err != nil {
			t.Fatal(err)
		}
		p, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
		if !reflect.DeepEqual(p.Body, []byte{0, 2}) || len(rest) != 0 {
			t.Fatalf("completed communication replay=%x", p.Body)
		}
		if err := s.handleCurrentFinishQuestWithResources(session, request, repos, catalog, tables, noItems); err != nil {
			t.Fatal(err)
		}
	}
	finalCharacter, _, _ := repos.Character.Load(ctx, "17")
	finalQuest, _, _ := repos.Quest.Load(ctx, "17")
	if !reflect.DeepEqual(finalCharacter, finishedCharacter) || !reflect.DeepEqual(finalQuest, completed) {
		t.Fatal("475/34 replay repeated reward or changed completed quest")
	}
}
