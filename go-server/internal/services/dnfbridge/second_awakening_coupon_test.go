package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSecondAwakeningCouponNativeRequest(t *testing.T) {
	r, ok := decodeCurrentQuestClearCouponRequest(testQuestClearCouponRequestBody(87, 121))
	if !ok || !r.ReservedValid || r.ActionIndex != 121 || r.SourceSlotIndex != 87 {
		t.Fatal("native second-awakening op515 action121 is not recognized")
	}
}

func TestSecondAwakeningCouponDefinition(t *testing.T) {
	base := "[stackable type] `[etc]` 1\n[action type] `[second awaken clear]` 0\n[minimum level] 75\n[use action packet] 1\n"
	for _, tail := range []string{"", "[need material] 99 80\n", "[action usable condition] `[unknown]`\n", "[impossible jobs] `gun blader`\n", "[impossible jobs] `unknown`\n", "[unable use grow type] 12 3 12 4\n", "[unable use grow type] 12 3 12\n", "[action usable place] `[dungeon]`\n", "[impossible contents] `ban redeemitem`\n[impossible contents] `hardcore mode`\n"} {
		doc, e := pvf.Parse("coupon.stk", base+tail)
		if e != nil {
			t.Fatal(e)
		}
		c, e := resolveSecondAwakeningCouponConditions(quest.ClearCoupon{Kind: quest.CouponSecondAwakening, MinimumLevel: 75}, doc)
		want := tail == "" || strings.Contains(tail, "need material") || strings.Contains(tail, "gun blader") || strings.Contains(tail, "12 3 12 4") || strings.Contains(tail, "hardcore mode")
		if (e == nil) != want {
			t.Fatalf("restriction %q accepted=%v", tail, e == nil)
		}
		if strings.Contains(tail, "hardcore mode") && !c.DisallowHardcore {
			t.Fatal("repeated content restriction omitted")
		}
		if strings.Contains(tail, "gun blader") && !reflect.DeepEqual(c.ExcludedJobs, []int{15}) {
			t.Fatal("job exclusion mismatch")
		}
	}
}

func TestSecondAwakeningCouponMatrixRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("current real PVF required")
	}
	s, _, g, _ := levelUpTicketBridgeFixture(t, 10162929, 90)
	ctx := context.Background()
	catalog, e := s.loadQuestCatalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	profiles, skills, e := s.currentProfessionResources(ctx)
	if e != nil {
		t.Fatal(e)
	}
	o, e := quest.NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	// IDs come from the actual stackable.lst; orphan .stk names are not IDs.
	ids := []int64{10005343, 10005939, 10006086, 10006419, 10006633, 10146823, 10147978, 10148493, 10149034, 10150599, 10150725, 10150753, 10150812, 10151252, 10151465, 10151834, 10153513, 10155945, 10156034, 10157170, 10157836, 10158864, 10159418, 10160338, 10161363, 10162422, 10162929, 10164039, 10164650, 10303235, 10303829, 10303916, 10304288, 490000008, 490000052, 490000112, 490000317, 490000734, 490000742, 490001857, 490001936, 490002879, 490003032, 490003185, 490003567, 490003901, 490004172, 490004368, 490005728, 490006070, 490008307}
	for _, id := range ids {
		t.Run("item_"+strconv.FormatInt(id, 10), func(t *testing.T) {
			c, e := resolveCurrentQuestCoupon(s.pvfItemCatalog, id, 121)
			if e != nil {
				t.Fatal(e)
			}
			if c.Kind != quest.CouponSecondAwakening || c.MinimumLevel != 75 && !(id == 10151465 && c.MinimumLevel == 0) {
				t.Fatal("wrong PVF action or level")
			}
			job := 2
			if len(c.Jobs) > 0 {
				job = c.Jobs[0]
			}
			character := repo.CharacterRecord{CharacterID: "29", AccountID: "hall:29", Job: strconv.Itoa(job), Level: 90, Stats: map[string]int64{"grow_type": 0x11, "gold": 42}}
			for _, e := range []error{g.Character.Save(ctx, character), g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "29", Points: repo.SkillPointState{SyncedLevel: 90, TotalSP: 100, RemainingSP: 70}}), g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "29"}), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:4": {ItemID: id, Count: 1}}})} {
				if e != nil {
					t.Fatal(e)
				}
			}
			r, e := o.UseClearCoupon(ctx, catalog, quest.UseClearCouponInput{AccountID: character.AccountID, CharacterID: "29", Slot: 4, Coupon: c, Now: time.Now(), Profiles: profiles, Skills: skills})
			if e != nil || !r.Awakened || r.Remaining != 0 || r.Character.Stats["grow_type"] != 0x21 {
				t.Fatalf("ticket %d failed: %v", id, e)
			}
		})
	}
	coupon, e := resolveCurrentQuestCoupon(s.pvfItemCatalog, 10162929, 121)
	if e != nil {
		t.Fatal(e)
	}
	branches, completeBranches, blockedBranches := 0, 0, 0
	for job := 0; job <= 15; job++ {
		if job == 9 || job == 10 {
			continue
		}
		for branch := 1; branch <= 15; branch++ {
			_, e := profiles.PlanTransition(byte(job), byte(0x10|branch), profession.Request{Kind: profession.KindAwakening, ChainType: 2, GrowNumber: 2, JobChangeQuest: 3})
			if e != nil {
				continue
			}
			branches++
			t.Run("job_"+strconv.Itoa(job)+"_branch_"+strconv.Itoa(branch), func(t *testing.T) {
				c := repo.CharacterRecord{CharacterID: "29", AccountID: "hall:29", Job: strconv.Itoa(job), Level: 90, Stats: map[string]int64{"grow_type": int64(0x10 | branch)}}
				for _, e := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "29", Points: repo.SkillPointState{SyncedLevel: 90}}), g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "29"}), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:4": {ItemID: 10162929, Count: 1}}})} {
					if e != nil {
						t.Fatal(e)
					}
				}
				r, e := o.UseClearCoupon(ctx, catalog, quest.UseClearCouponInput{AccountID: c.AccountID, CharacterID: "29", Slot: 4, Coupon: coupon, Now: time.Now(), Profiles: profiles, Skills: skills})
				missingTerminal := branch == 5 && (job == 0 || job == 2 || job == 3 || job == 4 || job == 5 || job == 6 || job == 11)
				if missingTerminal {
					blockedBranches++
					if e == nil || r.Changed {
						t.Fatal("placeholder branch lacking second-awakening quests must not mutate")
					}
					after, _, _ := g.Character.Load(ctx, "29")
					bag, _, _ := g.Inventory.Load(ctx, "29")
					if after.Stats["grow_type"] != int64(0x10|branch) || bag.Slots["0:4"].Count != 1 {
						t.Fatal("missing resources consumed ticket")
					}
					return
				}
				completeBranches++
				if e != nil || !r.Awakened || r.Character.Stats["grow_type"] != int64(0x20|branch) {
					t.Fatal("real profession/quest mismatch", e)
				}
				for _, id := range r.ChangedQuestIDs {
					d, ok := catalog.Find(id)
					if !ok || d.JobChangeQuest != 3 {
						t.Fatal("unrelated quest completed", id)
					}
				}
			})
		}
	}
	if branches != 64 || completeBranches != 57 || blockedBranches != 7 {
		t.Fatalf("review changed profile coverage: %d declarations / %d usable / %d incomplete", branches, completeBranches, blockedBranches)
	}
}

func TestSecondAwakeningCouponDispatchRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("current real PVF required")
	}
	for _, scenario := range []string{"success", "repeat", "bad_reserved", "wrong_action_item", "foreign_owner", "expired", "locked", "external", "not_ready", "write_failure", "reservation_released"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g, _ := levelUpTicketBridgeFixture(t, 10162929, 90)
			ctx := context.Background()
			p.loginPacketSeen = true
			c, _, _ := g.Character.Load(ctx, "29")
			c.Stats["grow_type"] = 0x11
			if scenario == "foreign_owner" {
				c.AccountID = "foreign"
			}
			if scenario == "external" {
				c.Job = "9"
				c.Stats["grow_type"] = 0x10
			}
			if e := g.Character.Save(ctx, c); e != nil {
				t.Fatal(e)
			}
			bag, _, _ := g.Inventory.Load(ctx, "29")
			stack := bag.Slots["0:4"]
			if scenario == "locked" {
				stack.Extra["equipment_lock_state"] = "locked"
			}
			if scenario == "expired" {
				stack.ExpireAt = time.Unix(1, 0)
				stack.Extra["expiration_source"] = "gm_override"
			}
			if scenario == "wrong_action_item" {
				stack.ItemID = 10151463
			}
			bag.Slots["0:4"] = stack
			if e := g.Inventory.Save(ctx, bag); e != nil {
				t.Fatal(e)
			}
			if scenario == "not_ready" {
				p.townSceneReadyCharacterID = 0
			}
			before := levelUpTicketBridgeSnapshot(t, g)
			req := testQuestClearCouponRequestBody(4, 121)
			if scenario == "bad_reserved" {
				req[11] = 1
			}
			if scenario == "write_failure" {
				p.conn = &secondCouponFailWriteConn{bufferConn: bufferConn{}}
			}
			if scenario == "reservation_released" {
				p.conn = &secondCouponReservationWriteConn{service: s, t: t}
			}
			handled, e := s.handleAlignedGameCommand(p, 1, 515, req)
			if !handled {
				t.Fatal("op515 not dispatched")
			}
			after := levelUpTicketBridgeSnapshot(t, g)
			if scenario == "reservation_released" {
				if e != nil || after[0].(repo.CharacterRecord).Stats["grow_type"] != 0x21 {
					t.Fatal("reservation release lost commit", e)
				}
				return
			}
			if scenario == "write_failure" {
				if e == nil || after[0].(repo.CharacterRecord).Stats["grow_type"] != 0x21 || after[1].(repo.InventoryRecord).Slots["0:4"].Count != 0 {
					t.Fatal("send failure lost committed state")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			packets := splitAllFinishBridgePackets(t, p.conn.(*bufferConn).write.Bytes())
			if scenario != "success" && scenario != "repeat" {
				if len(packets) != 1 || !bytes.Equal(packets[0].Body, []byte{0, 19, 4, 0, 0, 121}) || !reflect.DeepEqual(before, after) {
					t.Fatal("rejected use changed state or bad ACK")
				}
				return
			}
			if !bytes.Equal(packets[0].Body, []byte{1, 4, 0, 0, 121, 0, 0, 0}) || after[0].(repo.CharacterRecord).Stats["grow_type"] != 0x21 || after[1].(repo.InventoryRecord).Slots["0:4"].Count != 0 {
				t.Fatal("success ACK or persistent state mismatch")
			}
			ids := finishBridgePacketIDs(packets)
			t.Log("native projection sequence", ids)
			seen := map[uint16]bool{}
			for _, packet := range packets {
				seen[packet.Header.MsgID] = true
			}
			for _, op := range []uint16{14, 2, currentDungeonCharacterStateMsgID, currentSkillInfoMsgID} {
				if !seen[op] {
					t.Fatal("projection omitted", op)
				}
			}
			if scenario == "repeat" {
				p.conn.(*bufferConn).write.Reset()
				_, e = s.handleAlignedGameCommand(p, 1, 515, req)
				if e != nil || !reflect.DeepEqual(after, levelUpTicketBridgeSnapshot(t, g)) {
					t.Fatal("replay changed committed state")
				}
			}
		})
	}
}

type secondCouponFailWriteConn struct{ bufferConn }

func (*secondCouponFailWriteConn) Write([]byte) (int, error) { return 0, os.ErrClosed }

type secondCouponReservationWriteConn struct {
	bufferConn
	service *Service
	t       *testing.T
}

func (c *secondCouponReservationWriteConn) Write(b []byte) (int, error) {
	c.service.mu.Lock()
	held := len(c.service.characterAssetReservations)
	c.service.mu.Unlock()
	if held != 0 {
		c.t.Error("asset reservation held across socket projection")
	}
	return c.bufferConn.Write(b)
}
