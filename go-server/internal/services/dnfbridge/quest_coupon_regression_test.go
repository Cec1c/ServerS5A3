package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/jobmap"
	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestQuestCouponCurrentLevelAndRequestValidation(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"current_level", "pvf_level_cap", "minimum_level", "foreign_owner", "foreign_list", "negative_slot", "reserved0", "reserved1", "reserved2", "action_mismatch", "other_main_action"} {
		t.Run(scenario, func(t *testing.T) {
			service, session, repos := newQuestClearCouponTestService(t, 2)
			character, _, _ := repos.Character.Load(ctx, "17")
			character.Level = 35
			if scenario == "foreign_owner" {
				character.AccountID = "foreign"
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "17")
			stack := inv.Slots["0:87"]
			stack.ItemID = 10301410
			if scenario == "minimum_level" {
				stack.ItemID = 10164630
			}
			inv.Slots["0:87"] = stack
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if scenario == "pvf_level_cap" {
				source := testQuestClearCouponPVFSource()
				source["stackable/10301001/10301410.stk"] = "[stackable type] `[etc]` 1\n[action type] `[lv clear]` 20\n[/action type]\n"
				var err error
				service.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
				if err != nil {
					t.Fatal(err)
				}
			}
			request := testQuestClearCouponRequestBody(87, 179)
			switch scenario {
			case "minimum_level":
				binary.LittleEndian.PutUint32(request[7:11], 169)
			case "foreign_list":
				request[2] = 1
			case "negative_slot":
				binary.LittleEndian.PutUint16(request[:2], 0xffff)
			case "reserved0":
				request[3] = 1
			case "reserved1":
				request[11] = 1
			case "reserved2":
				request[15] = 1
			case "other_main_action":
				binary.LittleEndian.PutUint32(request[7:11], 169)
			case "action_mismatch":
				binary.LittleEndian.PutUint32(request[7:11], 118)
			}
			before, _, _ := repos.Quest.Load(ctx, "17")
			handled, err := service.handleCurrentQuestClearCouponUse(session, request)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			after, _, _ := repos.Quest.Load(ctx, "17")
			afterInv, _, _ := repos.Inventory.Load(ctx, "17")
			ack, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if scenario != "current_level" && scenario != "pvf_level_cap" {
				if ack.Body[0] != 0 || len(ack.Body) != 6 || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterInv, inv) {
					t.Fatal("invalid request changed state or returned success")
				}
				return
			}
			if !bytes.Equal(ack.Body, []byte{1, 87, 0, 0, 179, 0, 0, 0}) || afterInv.Slots["0:87"].Count != 1 {
				t.Fatalf("ack=%x count=%d", ack.Body, afterInv.Slots["0:87"].Count)
			}
			if after.States[100].Status != "completed" || after.States[102].Status != "active" {
				t.Fatalf("level-limited quests=%+v", after)
			}
			if scenario == "current_level" {
				if after.States[150].Status != "completed" {
					t.Fatal("eligible subquest omitted")
				}
				if after.States[101].Status != "completed" || after.States[106].Status != "completed" {
					t.Fatal("current level tasks omitted")
				}
			} else if after.States[150].Status == "completed" || after.States[101].Status == "completed" || after.States[106].Status != "active" {
				t.Fatal("PVF cap ignored")
			}
		})
	}
}

func TestRealPVFFirstAwakeningCouponAndSnapshots(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	profiles, skills, err := service.currentProfessionResources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quests, err := service.loadQuestCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	coupon, err := resolveCurrentQuestCoupon(catalog, 10164042, 118)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := quest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	// Every normal first branch must resolve a real class-specific awakening
	// terminal, without a guessed quest ID or second-awakening reward.
	for job := 0; job <= jobmap.MaxID; job++ {
		if job == 9 || job == 10 {
			continue
		}
		for branch := 1; branch <= 4; branch++ {
			t.Run(jobmap.QuestTags(job)[0]+strconv.Itoa(branch), func(t *testing.T) {
				c := repo.CharacterRecord{CharacterID: "77", AccountID: defaultAccountPrefix + "1", Name: "coupon", Job: strconv.Itoa(job), Level: 50, Stats: map[string]int64{"grow_type": int64(branch), "exp": 123, "gold": 777}}
				points := repo.SkillPointState{TotalSP: 100, RemainingSP: 70, SyncedLevel: 50}
				for _, e := range []error{repos.Character.Save(ctx, c), repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "77", Points: points}), repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 10164042, Count: 2}}}), repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "77"})} {
					if e != nil {
						t.Fatal(e)
					}
				}
				result, err := owner.UseClearCoupon(ctx, quests, quest.UseClearCouponInput{AccountID: c.AccountID, CharacterID: c.CharacterID, Slot: 87, Coupon: coupon, Now: time.Now(), Profiles: profiles, Skills: skills})
				if err != nil {
					t.Fatal(err)
				}
				if !result.Awakened || result.Character.Stats["grow_type"] != int64(0x10|branch) || result.Character.Stats["exp"] != 123 || result.Character.Stats["gold"] != 777 || result.Skill.Points != points || result.Remaining != 1 {
					t.Fatalf("result=%+v", result)
				}
				for _, id := range result.ChangedQuestIDs {
					def, ok := quests.Find(id)
					if !ok || def.JobChangeQuest != 2 {
						t.Fatalf("unexpected quest %d", id)
					}
				}
			})
		}
	}
	// Run the actual op515 route for swordman, including native mode0 actor,
	// state and skill projections. Client rendering still requires game testing.
	c, _, _ := repos.Character.Load(ctx, "77")
	c.Job = "0"
	c.Stats["grow_type"] = 1
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, repo.SkillRecord{CharacterID: "77", Points: repo.SkillPointState{SyncedLevel: 50}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "77"}); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 77}
	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(87, 118))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	packets := splitAllFinishBridgePackets(t, session.conn.(*bufferConn).write.Bytes())
	if len(packets) < 7 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketUseStackableAction) || !bytes.Equal(packets[0].Body, []byte{1, 87, 0, 0, 118, 0, 0, 0}) {
		t.Fatalf("packets=%v", finishBridgePacketIDs(packets))
	}
	var actor, state, skillIndex int
	for i, p := range packets {
		if p.Header.Classification != 0 {
			continue
		}
		switch p.Header.MsgID {
		case uint16(dnfenum.CmdPacketSetUDPIPPort):
			if p.Body[0] != 0 {
				t.Fatalf("unexpected personal-info mode %d", p.Body[0])
			}
			actor = i
		case currentDungeonCharacterStateMsgID:
			state = i
		case currentSkillInfoMsgID:
			skillIndex = i
		}
	}
	if actor <= 0 || state <= actor || skillIndex <= state {
		t.Fatalf("projection order actor=%d state=%d skills=%d", actor, state, skillIndex)
	}
}
