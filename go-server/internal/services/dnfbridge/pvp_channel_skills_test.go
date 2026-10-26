package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	pvp "longheng.io/server/internal/modules/dnf/pvp"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func TestPVPChannelSkillProjectionSurvivesClosedTrainingAndChannelRoundTrip(t *testing.T) {
	s, g, r, pk := dojoSlotTestScene(t, 6)
	g.pvpRoom = nil
	g.channel.Group, g.channel.Type = "pvp", 8
	g.townPositionSnapshot.AreaID = 0
	ctx := context.Background()
	c, _, _ := r.Character.Load(ctx, "31")
	c.Stats = map[string]int64{pvp.DojoSkillModeKey: 2}
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	ordinary, _, _ := r.Skill.Load(ctx, "31")
	for _, channel := range []string{"pvp", "normal", "pvp"} {
		g.channel.Group = channel
		projection, ready, err := s.buildCurrentSceneSkillInfoProjection(g, ctx, c, "频道往返技能验证")
		if err != nil || !ready {
			t.Fatalf("%s 技能投影: %v", channel, err)
		}
		slots := currentSkillInfoFirstTreeSlots(t, projection.body)
		want := uint16(47)
		if channel == "normal" {
			want = 46
		}
		if slots[6] != want {
			t.Fatalf("%s 快捷栏读错来源: %v，期望第7格=%d", channel, slots, want)
		}
	}
	after, _, _ := r.Skill.Load(ctx, "31")
	reloaded, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "31")
	if err != nil || !reflect.DeepEqual(after.Layouts, ordinary.Layouts) || !reflect.DeepEqual(reloaded.Layouts, pk.Layouts) {
		t.Fatal("切频道改写已保存的布局", err)
	}
}

func TestPVPChannelDragAfterTrainingWritesOnlyPKProfile(t *testing.T) {
	s, g, r, pk := dojoSlotTestScene(t, 6)
	g.pvpRoom = nil
	g.channel.Group, g.channel.Type = "pvp", 8
	g.townPositionSnapshot.AreaID = 0
	ctx := context.Background()
	c, _, _ := r.Character.Load(ctx, "31")
	c.Stats = map[string]int64{pvp.DojoSkillModeKey: 2}
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	ordinary, _, _ := r.Skill.Load(ctx, "31")
	handled, err := s.handleAlignedGameCommand(g, byte(dnfenum.GameCmdCommand), 28, []byte{0, 7, 6, 255, 255, 255, 255, 2})
	if err != nil || !handled {
		t.Fatal("PK频道拖动失败", err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 28 || len(rest) != 0 || packet.Body[0] != 1 {
		t.Fatal("拖动未成功应答")
	}
	after, _, _ := r.Skill.Load(ctx, "31")
	reloaded, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "31")
	if err != nil || !reflect.DeepEqual(after, ordinary) || reloaded.Layouts[0][198] != pk.Layouts[0][199] || !reflect.DeepEqual(reloaded.Layouts[1], pk.Layouts[1]) {
		t.Fatal("PK保存串页或写到刷图仓储", err)
	}
	peer, err := s.currentPVPPeerSkills(ctx, c)
	if err != nil || len(peer) < 2 || peer[1] != byte(len(reloaded.Skills)) {
		t.Fatal("对手技能没有取PK存档", err)
	}
}

func TestPVPChannelTreeSwitchCommitsPKIndexWithoutChangingOrdinaryIndex(t *testing.T) {
	s, g, r, _ := dojoSlotTestScene(t, 6)
	g.pvpRoom = nil
	g.channel.Group, g.channel.Type = "pvp", 8
	g.townPositionSnapshot.AreaID = 0
	ctx := context.Background()
	c, _, _ := r.Character.Load(ctx, "31")
	c.Stats = map[string]int64{pvp.DojoSkillModeKey: 2, "skill_tree_index": 0}
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	ordinary, _, _ := r.Skill.Load(ctx, "31")
	handled, err := s.handleAlignedGameCommand(g, byte(dnfenum.GameCmdCommand), 260, []byte{0, 0, 0, 0, 0})
	if err != nil || !handled {
		t.Fatal("PK切页未处理", err)
	}
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
	if ack.Header.MsgID != 260 || len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatal("PK切页未成功应答")
	}
	refresh, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
	if refresh.Header.MsgID != 19 || len(tail) != 0 {
		t.Fatal("PK切页后没有刷新独立两页")
	}
	reloaded, err := skillcmd.PrepareDojoSkills(ctx, r, g.accountID, "31")
	after, _, _ := r.Skill.Load(ctx, "31")
	character, _, _ := r.Character.Load(ctx, "31")
	if err != nil || reloaded.Trees.Active != 1 || !reflect.DeepEqual(after, ordinary) || character.Stats["skill_tree_index"] != 0 {
		t.Fatal("PK切页污染刷图加点或活动页", err)
	}
}
