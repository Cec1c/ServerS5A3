package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/jobmap"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestProfessionChangeCurrentWireAndPVFConfig(t *testing.T) {
	body := make([]byte, 14)
	body[13] = 3
	if slot, target, err := decodeCurrentProfessionChangeRequest(785, body); err != nil || slot != -1 || target != 3 {
		t.Fatal(slot, target, err)
	}
	if slot, target, err := decodeCurrentProfessionChangeRequest(502, []byte{3, 0, 3}); err != nil || slot != 3 || target != 3 {
		t.Fatal(slot, target, err)
	}
	for _, r := range []struct {
		op uint16
		b  []byte
	}{{785, make([]byte, 15)}, {785, make([]byte, 13)}, {502, []byte{255, 255, 3}}, {502, []byte{3, 0, 0}}, {502, []byte{3, 0, 3, 0}}} {
		if _, _, err := decodeCurrentProfessionChangeRequest(r.op, r.b); err == nil {
			t.Fatal("接受了其它版本或非法正文", r)
		}
	}
	for _, n := range []int64{0, 1, 9, 127, 128, 255, 2147483647} {
		b := buildCurrentProfessionChangeAck(22, n)
		if len(b) != 5 || binary.LittleEndian.Uint32(b) != 22 || b[4] > 127 {
			t.Fatal("次数被客户端按有符号字节解释", n, b)
		}
	}
	if !bytes.Equal(buildCurrentProfessionChangeAck(0, 2), []byte{0, 0, 0, 0, 2}) {
		t.Fatal("结果正文形状不符")
	}
	valid := "[grow up change lv] 15 49\n[/grow up change lv]\n[grow up change gold] 0 100113 133484 177979 237305 316406 421875 562500 750000 1000000\n[/grow up change gold]"
	for _, text := range []string{valid, strings.Replace(valid, "15 49", "15 14", 1), strings.Replace(valid, "0 100113", "1 100113", 1), strings.Replace(valid, "0 100113", "0 -1", 1), strings.Replace(valid, "133484", "1", 1)} {
		_, err := currentProfessionChangeConfig(levelTicketBridgeSource{"character/growup.etc": text})
		if (err == nil) != (text == valid) {
			t.Fatal("错误配置边界", text, err)
		}
	}
}

type levelTicketBridgeSource map[string]string

func (s levelTicketBridgeSource) ReadText(path string) (string, error) {
	if v, ok := s[path]; ok {
		return v, nil
	}
	return "", fmt.Errorf("缺少测试资源%s", path)
}

func TestProfessionChangeBookDefinitionBoundaries(t *testing.T) {
	valid := "[stackable type] `[waste]` 0\n[action type] `[class change]`\n[/action type]\n[icon] `Item/stackable/cash.img` 788\n[minimum level] 15\n[maximum level] 49\n[usable job] `[gunner]`\n[/usable job]\n[action usable place] `[village]` `[dont use assult]`\n[/action usable place]\n[action usable server type] `[general server]` `[event server]`\n[/action usable server type]"
	for _, tail := range []string{"", "\n[need material] 111 2", "\n[using limit info] 1", "\n[total usable count] 1", "\n[cooltime group] 1", "\n[action usable condition] `unknown`"} {
		d, err := pvf.Parse("stackable/test.stk", valid+tail)
		if err != nil {
			t.Fatal(err)
		}
		_, err = currentProfessionChangeBook(2683467, d.Path, d)
		if (err == nil) != (tail == "" || strings.Contains(tail, "need material")) {
			t.Fatal("额外条件被忽略", tail, err)
		}
	}
	for _, pair := range [][2]string{{"788", "9999"}, {"15\n[maximum", "50\n[maximum"}, {"[class change]`", "[class change]` 4"}, {"[village]", "[dungeon]"}, {"[general server]", "[unknown server]"}} {
		d, _ := pvf.Parse("stackable/test.stk", strings.Replace(valid, pair[0], pair[1], 1))
		if _, err := currentProfessionChangeBook(2683467, d.Path, d); err == nil {
			t.Fatal("非法物品定义被接受", pair)
		}
	}
	for _, onlyStarter := range []bool{false, true} {
		serverTypes := "`[starter server]` `[general server]` `[event server]`"
		if onlyStarter {
			serverTypes = "`[starter server]`"
		}
		d, _ := pvf.Parse("stackable/test.stk", strings.Replace(valid, "`[general server]` `[event server]`", serverTypes, 1))
		_, err := currentProfessionChangeBook(2660701, d.Path, d)
		if (err == nil) == onlyStarter {
			t.Fatal("把额外开放新手服误判为仅限新手服", onlyStarter, err)
		}
	}
}

func prepareProfessionRealCharacter(t *testing.T, s *Service, g repo.Group, job int, grow byte, level int, item int64) {
	t.Helper()
	ctx := context.Background()
	c, _, _ := g.Character.Load(ctx, "29")
	c.Job = strconv.Itoa(job)
	c.Level = level
	c.Stats["grow_type"] = int64(grow)
	c.Stats["gold"] = 3000000
	delete(c.Stats, inventory.ProfessionChangeCountKey)
	delete(c.Stats, "profession_return_group")
	delete(c.Stats, "profession_return_grow")
	delete(c.Stats, "profession_return_from")
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := g.Skill.Load(ctx, "29")
	points, err := s.currentPVFSkillPointTarget(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	skill.Points = points
	if err := g.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:4": {ItemID: item, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}, "0:5": {ItemID: 10002567, Count: 8}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Quest.Save(ctx, repo.QuestRecord{CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}
}

func TestProfessionChangeAllBooksRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要现用真实PVF")
	}
	a := levelUpTicketRealArchive(t)
	s, p, g, _ := levelUpTicketBridgeFixture(t, 2683467, 25)
	profiles, skills, err := s.currentProfessionResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	list, err := a.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	lst, err := pvf.Parse("stackable/stackable.lst", list)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range pvf.ParseList(lst) {
		path := "stackable/" + strings.TrimPrefix(entry.Path, "stackable/")
		text, err := a.ReadText(path)
		if err != nil || !strings.Contains(text, "`[class change]`") {
			continue
		}
		count++
		t.Run(strconv.FormatInt(entry.ID, 10), func(t *testing.T) {
			d, err := pvf.Parse(path, text)
			if err != nil {
				t.Fatal(err)
			}
			b, err := currentProfessionChangeBook(entry.ID, path, d)
			if err != nil {
				t.Fatal("真实书未接入", err)
			}
			stage := byte(0)
			if b.RequireAwakening {
				stage = 1
			}
			job, from, to := -1, byte(0), byte(0)
			for j := 0; j <= jobmap.MaxID && job < 0; j++ {
				allowed := false
				for _, tag := range b.Jobs {
					allowed = allowed || jobmap.MatchesQuestTag(tag, j) || tag == "[all]"
				}
				if !allowed {
					continue
				}
				for f := byte(1); f <= 5 && job < 0; f++ {
					for target := byte(1); target <= 5; target++ {
						if b.ReturnBranch > 0 && target != b.ReturnBranch {
							continue
						}
						if len(b.AllowedBranches) > 0 {
							ok1, ok2 := false, false
							for _, v := range b.AllowedBranches {
								ok1 = ok1 || v == f
								ok2 = ok2 || v == target
							}
							if !ok1 || !ok2 {
								continue
							}
						}
						if _, err := profiles.PlanBranchChange(skills, byte(j), stage<<4|f, target, b.PreserveAwakening); err == nil {
							job, from, to = j, stage<<4|f, target
							break
						}
					}
				}
			}
			if job < 0 {
				t.Fatal("当前PVF没有可执行的合法分支", entry.ID, b)
			}
			prepareProfessionRealCharacter(t, s, g, job, from, b.MinimumLevel, entry.ID)
			if b.ReturnBranch > 0 {
				c, _, _ := g.Character.Load(context.Background(), "29")
				c.Stats["profession_return_group"] = b.ReturnGroup
				c.Stats["profession_return_grow"] = int64(stage<<4 | to)
				c.Stats["profession_return_from"] = int64(from)
				if err := g.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			p.conn.(*bufferConn).write.Reset()
			handled, err := currentGameplayModules.DispatchUpper(s, p, uint16(dnfenum.CmdPacketUseRightOfChangeGrowType), 1, []byte{4, 0, to})
			if !handled || err != nil {
				t.Fatal("生产路由失败", handled, err)
			}
			c, _, _ := g.Character.Load(context.Background(), "29")
			bag, _, _ := g.Inventory.Load(context.Background(), "29")
			sk, _, _ := g.Skill.Load(context.Background(), "29")
			if c.Stats["grow_type"] != int64(stage<<4|to) || c.Stats["gold"] != 3000000 || c.Stats[inventory.ProfessionChangeCountKey] != 0 || bag.Slots["0:4"].ItemID == entry.ID || bag.Slots["0:5"].Count != 8 || sk.Points.RemainingSP != sk.Points.TotalSP {
				t.Fatal("转职或消费结果错误", entry.ID, c.Stats, bag)
			}
			if entry.ID == 2683469 || entry.ID == 2683470 {
				bonus := entry.ID + 2
				found := false
				for _, st := range bag.Slots {
					if st.ItemID == bonus && st.Count == 1 {
						left := st.ExpireAt.Sub(s.gameplayNow()).Hours()
						if left < 359 || left > 361 {
							t.Fatal("赠书不是15天实例期限", st)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("特别版未附送转职书")
				}
			}
			var ops []uint16
			data := p.conn.(*bufferConn).write.Bytes()
			var lastBody []byte
			var lastClass byte
			for len(data) > 0 {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
				data = rest
				ops = append(ops, packet.Header.MsgID)
				lastBody = packet.Body
				lastClass = packet.Header.Classification
			}
			if len(ops) < 7 || ops[0] != 2 || ops[1] != 37 || ops[len(ops)-2] != 1150 || ops[len(ops)-1] != 502 || lastClass != 1 || !bytes.Equal(lastBody, []byte{1}) {
				t.Fatal("转职投影和应答顺序不符", ops, lastBody)
			}
		})
	}
	if count != 54 {
		t.Fatal("真实转职书数量变化，需重新核对", count)
	}
}

func TestProfessionChangeSystemAndReconnectInfoRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要现用真实PVF")
	}
	s, p, g, _ := levelUpTicketBridgeFixture(t, 2683467, 25)
	prepareProfessionRealCharacter(t, s, g, 2, 1, 25, 2683467)
	body := make([]byte, 14)
	body[13] = 2
	for i := 0; i < 2; i++ {
		if i == 1 {
			body[13] = 1
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentProfessionChange(p, 785, body); err != nil {
			t.Fatal(err)
		}
		c, _, _ := g.Character.Load(context.Background(), "29")
		wantGold := int64(3000000)
		if i == 1 {
			wantGold -= 100113
		}
		if c.Stats["gold"] != wantGold || c.Stats[inventory.ProfessionChangeCountKey] != int64(i+1) {
			t.Fatal("首次免费或后续收费错误", c)
		}
	}
	c, _, _ := g.Character.Load(context.Background(), "29")
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentProfessionChangeInfo(p, c); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 1150 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{2, 25, 0, 0, 0}) {
		t.Fatal("重登次数/等级状态错误", packet.Body)
	}
}

func TestProfessionChangeAdvancedAndSpecialBooksKeepRealAwakening(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要现用真实PVF")
	}
	for _, row := range []struct {
		id    int64
		level int
		grow  byte
	}{{2683468, 60, 0x11}, {2683631, 85, 0x21}, {10100292, 90, 0x21}} {
		t.Run(strconv.FormatInt(row.id, 10), func(t *testing.T) {
			s, p, g, _ := levelUpTicketBridgeFixture(t, row.id, row.level)
			prepareProfessionRealCharacter(t, s, g, 2, row.grow, row.level, row.id)
			if err := s.handleCurrentProfessionChange(p, 502, []byte{4, 0, 2}); err != nil {
				t.Fatal(err)
			}
			c, _, _ := g.Character.Load(context.Background(), "29")
			if c.Stats["grow_type"] != int64(row.grow&0xf0|2) {
				t.Fatal("高级或特级书丢失觉醒", c.Stats)
			}
			profiles, skills, err := s.currentProfessionResources(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			grants, err := profiles.ResetGrants(skills, 2, byte(c.Stats["grow_type"]), c.Level)
			if err != nil {
				t.Fatal(err)
			}
			sk, _, _ := g.Skill.Load(context.Background(), "29")
			if len(sk.Skills) != len(grants) {
				t.Fatal("旧分支技能未清理", sk.Skills, grants)
			}
			for _, grant := range grants {
				if sk.Skills[int64(grant.SkillID)].Level != grant.Level {
					t.Fatal("目标觉醒技能没有按实际等级重建", grant)
				}
			}
		})
	}
}

type professionChangeFailConn struct {
	*bufferConn
	fail bool
}

func (c *professionChangeFailConn) Write(b []byte) (int, error) {
	if c.fail {
		c.fail = false
		return 0, io.ErrClosedPipe
	}
	return c.bufferConn.Write(b)
}

func TestProfessionChangeWriteFailureResumesWithoutDebitRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("需要现用真实PVF")
	}
	s, p, g, _ := levelUpTicketBridgeFixture(t, 2683467, 25)
	prepareProfessionRealCharacter(t, s, g, 2, 1, 25, 2683467)
	conn := &professionChangeFailConn{bufferConn: p.conn.(*bufferConn), fail: true}
	p.conn = conn
	if err := s.handleCurrentProfessionChange(p, 502, []byte{4, 0, 2}); err == nil || p.professionChangePending == nil {
		t.Fatal("写失败未保留已提交投影", err)
	}
	before := levelUpTicketBridgeSnapshot(t, g)
	if err := s.handleCurrentProfessionChange(p, 502, []byte{4, 0, 2}); err != nil {
		t.Fatal(err)
	}
	if p.professionChangePending != nil || !reflect.DeepEqual(before, levelUpTicketBridgeSnapshot(t, g)) {
		t.Fatal("补投重新扣书或修改职业")
	}
}
