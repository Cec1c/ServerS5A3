package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestDarkKnightIsolationOtherJobsKeepTwelveSlotsAndCustomCommands(t *testing.T) {
	for job := 0; job <= jobmap.MaxID; job++ {
		if job == int(darkcombo.Job) {
			continue
		}
		for _, active := range []byte{0, 1} {
			t.Run(fmt.Sprintf("job_%d_page_%d", job, active), func(t *testing.T) {
				states := make(map[int64]dnfrepo.SkillState)
				other := make(map[int64]dnfrepo.SkillState)
				layouts := map[int]dnfrepo.SkillLayout{0: {}, 1: {}}
				for slot := 0; slot < 12; slot++ {
					// 118..123与黑武根技能同号，普通职业不能因此被识别为连招根。
					states[int64(118+slot)] = dnfrepo.SkillState{Level: 1, Enabled: true}
					other[int64(118+slot)] = dnfrepo.SkillState{Level: 2, Enabled: true}
					internal := slot
					if slot >= 6 {
						internal += 192
					}
					layouts[0][internal] = uint16(118 + slot)
					layouts[1][internal] = uint16(129 - slot)
				}
				record := dnfrepo.SkillRecord{CharacterID: "77", Skills: states, Layouts: layouts,
					Points: dnfrepo.SkillPointState{RemainingSP: 91, RemainingTP: 7},
					Trees: dnfrepo.SkillTreeState{Expanded: true, Active: active,
						Other: dnfrepo.SkillProfile{Skills: other, Points: dnfrepo.SkillPointState{RemainingSP: 43, RemainingTP: 3}}}}
				before := dnfrepo.CloneSkill(record)
				character := dnfrepo.CharacterRecord{CharacterID: "77", Job: strconv.Itoa(job)}
				service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
				command := []byte{0x40, 0x80}
				body, quick, err := service.buildComboAwareCommittedSkillBody(context.Background(), character, record, map[uint16][]byte{118: command})
				if err != nil || len(quick) != 12 {
					t.Fatalf("普通职业十二格投影失败：quick=%v err=%v", quick, err)
				}
				_, message := consumeCurrentSkillInfoFields(t, body[4:])
				if len(message[2]) != 2 {
					t.Fatal("普通职业双技能页丢失")
				}
				for page, tree := range message[2] {
					points, rows := consumeCurrentSkillInfoFields(t, tree)
					wantSP, wantTP, wantLevel := uint64(91), uint64(7), uint64(1)
					if byte(page) != active {
						wantSP, wantTP, wantLevel = 43, 3, 2
					}
					if len(rows[3]) != 12 || points[1][0] != wantSP || points[2][0] != wantTP {
						t.Fatalf("第%d页技能或SP/TP被连招逻辑改变", page)
					}
					for slot, row := range rows[3] {
						fields, vectors := consumeCurrentSkillInfoFields(t, row)
						wantID := 118 + slot
						if page == 1 {
							wantID = 129 - slot
						}
						if fields[1][0] != uint64(slot) || fields[2][0] != uint64(wantID) || fields[3][0] != wantLevel || len(fields[4]) != 0 {
							t.Fatalf("第%d页槽%d被连招替换：fields=%v", page, slot, fields)
						}
						if wantID == 118 && (len(vectors[4]) != 1 || !bytes.Equal(vectors[4][0], command)) {
							t.Fatal("与黑武根同号的普通技能自定义指令丢失")
						}
					}
				}
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 77}
				if err := service.sendCommittedDarkKnightSkillRefresh(session, character, record, "职业隔离回归"); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.MsgID != currentSkillInfoMsgID || packet.Header.Classification != 0 || len(rest) != 0 {
					t.Fatal("普通职业技能刷新额外发送了黑武连招通知")
				}
				if !reflect.DeepEqual(before, record) {
					t.Fatal("普通职业投影修改了布局、加点或另一技能页")
				}
			})
		}
	}
}

func TestDarkKnightIsolationOtherJobsRejectComboAfterCharacterSwitch(t *testing.T) {
	for job := 0; job <= jobmap.MaxID; job++ {
		if job == int(darkcombo.Job) {
			continue
		}
		t.Run(fmt.Sprintf("job_%d", job), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "78", AccountID: "acc", Slot: 1, Job: strconv.Itoa(job), Level: 90}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			record := currentDarkKnightComboNativeTestRecord("78")
			if err := repos.Skill.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			page := darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}
			if err := darkcombo.SavePages(ctx, repos.Settings, "78", []darkcombo.Page{page}, time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, darkcombo.SettingsScope("78"))
			conn := &bufferConn{}
			service := &Service{options: options{accountID: "acc", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, accountID: "acc", selectedCharacterID: 77}
			session.darkKnightCombo.arm(77, 0, time.Now().Add(time.Minute))
			session.darkKnightCombo.recordProjection(77, []darkcombo.Page{page}, time.Now())
			session.darkKnightCombo.finalTownSceneInitialization = true
			service.bindGameSessionCharacter(session, 78)
			if session.darkKnightCombo.pendingCharacterID != 0 || session.darkKnightCombo.projectionCharacterID != 0 || session.darkKnightCombo.finalTownSceneInitialization {
				t.Fatal("切换普通职业后保留了黑武连招会话状态")
			}
			if err := service.sendCurrentDarkKnightComboInitialization(session, ctx, character, "职业隔离回归"); err != nil {
				t.Fatal(err)
			}
			if err := service.sendCurrentDarkKnightComboPreload(session, ctx, []dnfrepo.CharacterRecord{character}, "职业隔离回归"); err != nil || conn.write.Len() != 0 {
				t.Fatalf("普通职业收到447/809：err=%v bytes=%x", err, conn.write.Bytes())
			}
			body, err := darkcombo.EncodePageBlock(page)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.handleCurrentDarkKnightComboSkillInfo(session, body); err != nil {
				t.Fatal(err)
			}
			if err := service.handleCurrentDarkKnightComboQuickSlotReset(session, []byte{0, 2}); err != nil {
				t.Fatal(err)
			}
			packets := splitAllCurrentUpperPackets(t, conn.write.Bytes())
			if len(packets) != 2 {
				t.Fatalf("普通职业拒绝连招时额外发送了通知：%d", len(packets))
			}
			for i, opcode := range []uint16{uint16(dnfenum.CmdPacketComboSkillInfo), uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset)} {
				if packets[i].Header.MsgID != opcode || packets[i].Header.Classification != 1 ||
					!bytes.Equal(packets[i].Body, []byte{0, currentDarkKnightComboFailureCode}) {
					t.Fatalf("普通职业连招请求未拒绝：%+v body=%x", packets[i].Header, packets[i].Body)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, darkcombo.SettingsScope("78"))
			skills, _, _ := repos.Skill.Load(ctx, "78")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(record, skills) || session.darkKnightCombo.pendingCharacterID != 0 {
				t.Fatal("普通职业的布局、技能或设置被黑武请求修改")
			}
		})
	}
}

func TestRealPVFDarkKnightIsolationOtherJobsKeepActivePassiveAndTPPlacement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	for job := byte(0); job <= jobmap.MaxID; job++ {
		if job == darkcombo.Job {
			continue
		}
		t.Run(fmt.Sprintf("job_%d", job), func(t *testing.T) {
			states := make(map[int64]dnfrepo.SkillState)
			actives, passives, tps := 0, 0, 0
			for _, definition := range catalog.SkillsForJob(job) {
				switch {
				case definition.IsTPSkill() && tps < 1:
					tps++
				case definition.Active && !definition.IsTPSkill() && actives < 12:
					actives++
				case !definition.Active && !definition.IsTPSkill() && passives < 3:
					passives++
				default:
					continue
				}
				states[int64(definition.ID)] = dnfrepo.SkillState{Level: 1, Enabled: true}
			}
			if actives == 0 {
				t.Fatalf("现用PVF没有job%d的主动技能，无法验证其快捷栏", job)
			}
			for _, tree := range []int{0, 1} {
				layout, err := skillcmd.BuildCurrentSkillLayout(catalog, job, tree, states, nil)
				if err != nil {
					t.Fatal(err)
				}
				record := dnfrepo.SkillRecord{Skills: states, Layouts: map[int]dnfrepo.SkillLayout{0: layout}}
				body, quick, err := buildCurrentSceneSkillInfoBody(record, layout, nil)
				if err != nil || len(quick) != actives {
					t.Fatalf("第%d页普通主动未填满可用十二格：quick=%v active=%d err=%v", tree, quick, actives, err)
				}
				for slot, id := range currentSkillInfoFirstTreeSlots(t, body) {
					definition, _ := catalog.Find(job, id)
					if slot < 12 && (!definition.Active || definition.IsTPSkill()) {
						t.Fatalf("被动/TP误入快捷栏%d：skill=%d", slot, id)
					}
				}
				initial, err := skillcmd.BuildInitialSkillLayout(catalog, job, tree, states)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for slot := range initial {
					if slot < 6 || slot >= 198 {
						count++
					}
				}
				if count != min(3, actives) {
					t.Fatalf("首次布局超出既有前三主动规则：quick=%d", count)
				}
				if rebuilt, err := skillcmd.BuildCurrentSkillLayout(catalog, job, tree, states, layout); err != nil || !reflect.DeepEqual(layout, rebuilt) {
					t.Fatalf("重登重建改变了普通职业已有布局：err=%v", err)
				}
			}
			t.Logf("真实PVF：主动%d，被动%d，TP%d；两页均验证", actives, passives, tps)
		})
	}
}
