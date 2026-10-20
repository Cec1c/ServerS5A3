package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func guildFoundationFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group) {
	t.Helper()
	r := memory.NewMemoryGroup()
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, channelAdvertiseID: 1}, repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }, gameSessions: map[uint16]*gameSession{}}
	var sessions []*gameSession
	for i := 1; i <= 3; i++ {
		c := dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: fmt.Sprint("a", i), Name: fmt.Sprint("角色", i), Job: "1", Level: 90, Stats: map[string]int64{"gold": 900000, "grow_type": 33, "expert_job_type": 3, "expert_job_exp": 36}}
		if err := r.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		peer := &gameSession{conn: &bufferConn{}, accountID: c.AccountID, selectedCharacterID: uint16(i), characterGeneration: uint64(i), townActorOwnerChannel: 10, townSceneReadyCharacterID: uint16(i)}
		s.gameSessions[uint16(i)] = peer
		sessions = append(sessions, peer)
	}
	t.Cleanup(func() {
		for _, p := range sessions {
			s.closeCurrentGuildCreateSession(p)
		}
	})
	return s, sessions, r
}

func guildWireString(s string) []byte {
	var w packetWriter
	writeClientNameDstr(&w, s)
	return w.bytes()
}
func guildDispatch(t *testing.T, s *Service, p *gameSession, op uint16, b []byte) {
	t.Helper()
	handled, err := currentGameplayModules.DispatchUpper(s, p, op, 1, b)
	if !handled || err != nil {
		t.Fatalf("dispatch op%d handled=%v err=%v", op, handled, err)
	}
}
func guildReply(t *testing.T, p *gameSession, op uint16) []byte {
	t.Helper()
	var result []byte
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == op {
			result = append([]byte(nil), packet.Body...)
		}
	}
	if result == nil {
		t.Fatalf("missing op%d", op)
	}
	return result
}
func guildPrepareInvite(t *testing.T, s *Service, p []*gameSession) {
	guildDispatch(t, s, p[0], 156, guildWireString("测试公会"))
	guildDispatch(t, s, p[0], 739, guildWireString("共同冒险"))
	guildDispatch(t, s, p[0], 741, append([]byte{byte(s.channelAdvertiseID())}, guildWireString("角色2")...))
	if b := guildReply(t, p[0], 741); !bytes.Equal(b, []byte{1}) {
		t.Fatalf("permit=%x", b)
	}
	if b := guildReply(t, p[1], 685); !bytes.Contains(b, rosterNameBytes("测试公会")) {
		t.Fatalf("invite=%x", b)
	}
}

func TestGuildFoundationNativeCreateJoinAndRelog(t *testing.T) {
	s, p, r := guildFoundationFixture(t)
	ctx := context.Background()
	guildPrepareInvite(t, s, p)
	guildDispatch(t, s, p[0], 68, []byte{1}) // consent is mandatory
	if b := guildReply(t, p[0], 68); b[0] != 0 {
		t.Fatalf("created without consent: %x", b)
	}
	guildDispatch(t, s, p[1], 742, []byte{1})
	p[0].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, p[0], 68, []byte{1})
	wire := p[0].conn.(*bufferConn).write.Bytes()
	first, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
	if first.Header.MsgID != 68 || !bytes.Equal(first.Body, []byte{1}) {
		t.Fatalf("creation ACK=%v %x", first.Header, first.Body)
	}
	actorSeen := false
	for len(rest) > 0 {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
		rest = next
		if packet.Header.MsgID == 2 && len(packet.Body) > 70 && packet.Body[0] == 0 {
			assertGuildFoundationActor(t, packet.Body, 1, "测试公会")
			actorSeen = true
		}
		if packet.Header.MsgID == 308 && !actorSeen {
			t.Fatal("membership before actor")
		}
	}
	if !actorSeen {
		t.Fatal("no guild actor refresh")
	}
	guildDispatch(t, s, p[0], 68, []byte{1})
	c, _, _ := r.Character.Load(ctx, "1")
	if c.Stats["gold"] != 600000 {
		t.Fatalf("retry debit=%d", c.Stats["gold"])
	}
	reg, err := dnfguild.Load(ctx, r.Settings)
	if err != nil || len(reg.Guilds) != 1 || len(reg.Guilds[1].Members) != 2 {
		t.Fatalf("state=%+v err=%v", reg, err)
	}
	guildDispatch(t, s, p[2], 348, append(guildWireString("测试公会"), guildWireString("加入")...))
	guildDispatch(t, s, p[2], 348, append(guildWireString("测试公会"), guildWireString("加入")...))
	reg, err = dnfguild.Load(ctx, r.Settings)
	if err != nil || len(reg.Guilds[1].Members) != 3 || len(reg.Guilds[1].Applications) != 0 {
		t.Fatalf("join=%+v err=%v", reg, err)
	}
	// Fresh service and connection, only durable repository state survives.
	fresh := &Service{options: s.options, repositoryProvider: s.repositoryProvider, gameSessions: map[uint16]*gameSession{}}
	relog := &gameSession{conn: &bufferConn{}, accountID: "a3", selectedCharacterID: 3, characterGeneration: 91, initialTownLegacySceneReadyAccepted: true, townActorOwnerChannel: 10, townSceneReadyCharacterID: 3}
	fresh.gameSessions[3] = relog
	if err := fresh.sendCurrentInitialTownGuildStateProjectionLocked(relog, "test_relog"); err != nil {
		t.Fatal(err)
	}
	before := relog.conn.(*bufferConn).write.Len()
	if err := fresh.sendCurrentInitialTownGuildStateProjectionLocked(relog, "retry"); err != nil {
		t.Fatal(err)
	}
	if before != relog.conn.(*bufferConn).write.Len() {
		t.Fatal("replayed login projection")
	}
	guildDispatch(t, fresh, relog, 140, nil)
	list := guildReply(t, relog, 140)
	for _, name := range []string{"角色1", "角色2", "角色3"} {
		if !bytes.Contains(list, rosterNameBytes(name)) {
			t.Fatalf("roster lacks %s", name)
		}
	}
}

func assertGuildFoundationActor(t *testing.T, body []byte, id uint32, name string) {
	t.Helper()
	_, start, ok := currentSceneObjectLevelForLog(body)
	if !ok {
		t.Fatal("missing tail")
	}
	tail := body[start:]
	equipEnd, ok := currentSceneObjectEquipSummaryEnd(tail, 6)
	if !ok {
		t.Fatal("equipment boundary")
	}
	if got := binary.LittleEndian.Uint32(tail[equipEnd+17:]); got != id {
		t.Fatalf("guild ID=%d", got)
	}
	if !bytes.Contains(tail, rosterNameBytes(name)) {
		t.Fatal("missing guild name")
	}
	if event, ok := currentSceneObjectTailActorStateEventArgForLog(body); !ok || event != currentSceneActorStateEventArg {
		t.Fatalf("guild shifted actor fields: event=%d ok=%v", event, ok)
	}
	if visual, ok := currentSceneObjectTailVisualStateForLog(body); !ok || visual != currentSceneNormalTownVisualState {
		t.Fatalf("guild shifted visual state=%d ok=%v", visual, ok)
	}
}

func TestGuildFoundationConsentInvalidation(t *testing.T) {
	for _, action := range []string{"reject", "expiry", "disconnect", "replace", "rename", "foreign-region"} {
		t.Run(action, func(t *testing.T) {
			s, p, r := guildFoundationFixture(t)
			guildPrepareInvite(t, s, p)
			switch action {
			case "reject":
				guildDispatch(t, s, p[1], 742, []byte{0})
			case "expiry":
				s.expireCurrentGuildCreateInvitation(p[0].guild.createInvitation)
			case "disconnect":
				s.unbindGameSession(p[1])
			case "replace":
				s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}, accountID: "a2"}, 2)
			case "rename":
				guildDispatch(t, s, p[0], 156, guildWireString("另一个公会"))
			case "foreign-region":
				s.closeCurrentGuildCreateSession(p[0])
				guildDispatch(t, s, p[0], 156, guildWireString("测试公会"))
				guildDispatch(t, s, p[0], 739, guildWireString("欢迎"))
				guildDispatch(t, s, p[0], 741, append([]byte{249}, guildWireString("角色2")...))
			}
			guildDispatch(t, s, p[0], 68, []byte{1})
			reg, err := dnfguild.Load(context.Background(), r.Settings)
			if err != nil || len(reg.Guilds) != 0 {
				t.Fatalf("invalid consent committed: %+v %v", reg, err)
			}
			c, _, _ := r.Character.Load(context.Background(), "1")
			if c.Stats["gold"] != 900000 {
				t.Fatal("invalid consent charged")
			}
		})
	}
}

func TestGuildFoundationApprovalFailuresKeepNativeIdentityTail(t *testing.T) {
	s, p, _ := guildFoundationFixture(t)
	for _, body := range [][]byte{nil, {3, 0, 0, 0, 0}, {3, 0, 0, 0, 249}} {
		p[0].conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p[0], 350, body)
		got := guildReply(t, p[0], 350)
		if len(got) != 7 || got[0] != 0 {
			t.Fatalf("failure missing callback identity: %x", got)
		}
	}
}

func TestGuildFoundationCreationReplyLossDoesNotChargeAgain(t *testing.T) {
	s, p, r := guildFoundationFixture(t)
	guildPrepareInvite(t, s, p)
	guildDispatch(t, s, p[1], 742, []byte{1})
	want := errors.New("lost creation ACK")
	p[0].conn = &failNthDungeonWriteConn{failAt: 1, err: want}
	if err := s.handleCurrentGuildCreate(p[0], []byte{1}); !errors.Is(err, want) {
		t.Fatalf("write error=%v", err)
	}
	p[0].conn = &bufferConn{}
	p[1].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, p[0], 68, []byte{1})
	c, _, _ := r.Character.Load(context.Background(), "1")
	if c.Stats["gold"] != 600000 {
		t.Fatal("reply loss charged twice")
	}
	if b := guildReply(t, p[1], 308); b[0] != 1 {
		t.Fatalf("founder recovery=%x", b)
	}
}

func TestGuildFoundationApplicationApprovalIsScopedAndDurable(t *testing.T) {
	s, p, r := guildFoundationFixture(t)
	ctx := context.Background()
	guildPrepareInvite(t, s, p)
	guildDispatch(t, s, p[1], 742, []byte{1})
	guildDispatch(t, s, p[0], 68, []byte{1})
	err := dnfguild.Update(ctx, r.Guild, []dnfrepo.GuildOwner{{AccountID: "a1", CharacterID: "1"}}, s.gameplayNow(), func(_ dnfrepo.Group, reg *dnfguild.Registry) error { reg.Guilds[1].JoinMode = 0; return nil })
	if err != nil {
		t.Fatal(err)
	}
	guildDispatch(t, s, p[2], 348, append(guildWireString("测试公会"), guildWireString("请批准")...))
	guildDispatch(t, s, p[1], 350, []byte{3, 0, 0, 0, 0}) // ordinary member cannot approve
	reg, err := dnfguild.Load(ctx, r.Settings)
	if err != nil || len(reg.Guilds[1].Members) != 2 || len(reg.Guilds[1].Applications) != 1 {
		t.Fatalf("unauthorized approval %+v %v", reg, err)
	}
	guildDispatch(t, s, p[0], 350, []byte{3, 0, 0, 0, 1}) // directory is not applicant identity
	if b := guildReply(t, p[0], 350); b[0] != 0 {
		t.Fatal("directory aliased social group")
	}
	guildDispatch(t, s, p[0], 350, []byte{3, 0, 0, 0, 0})
	reg, err = dnfguild.Load(ctx, r.Settings)
	if err != nil || len(reg.Guilds[1].Members) != 3 || len(reg.Guilds[1].Applications) != 0 {
		t.Fatalf("approval %+v %v", reg, err)
	}
	for _, id := range []string{"2", "3"} {
		c, _, _ := r.Character.Load(ctx, id)
		if c.Stats["gold"] != 900000 {
			t.Fatal("joining changed member gold")
		}
	}
}

func TestGuildFoundationCurrentOpcodeAndRosterConsumption(t *testing.T) {
	s, p, _ := guildFoundationFixture(t)
	guildPrepareInvite(t, s, p)
	guildDispatch(t, s, p[1], 742, []byte{1})
	guildDispatch(t, s, p[0], 68, []byte{1})
	for _, op := range []uint16{1450, 1451, 1452, 1453} {
		p[0].conn.(*bufferConn).write.Reset()
		var req []byte
		if op == 1450 {
			req = []byte{2, 0, 0, 0, 0, 0}
		}
		guildDispatch(t, s, p[0], op, req)
		b := guildReply(t, p[0], op)
		if b[0] != 1 {
			t.Fatalf("op%d failed=%x", op, b)
		}
		if op == 1452 && (binary.LittleEndian.Uint32(b[1:]) != 1 || binary.LittleEndian.Uint16(b[5:]) != 2) {
			t.Fatalf("op1452 must be grade roster, not search=%x", b)
		}
		if op == 1452 && b[9] != 0 {
			t.Fatal("grade identity changed to directory")
		}
		if op == 1453 && !bytes.Equal(b, []byte{1, 1, 0, 0, 0, 0, 0}) {
			t.Fatalf("op1453 must terminate grade paging=%x", b)
		}
	}
	guildDispatch(t, s, p[0], 140, nil)
	b := guildReply(t, p[0], 140)
	at := 5
	_, at, ok := readCurrentRawDstr(b, at)
	if !ok {
		t.Fatal("guild name")
	}
	at += 2
	count := int(binary.LittleEndian.Uint16(b[at:]))
	at += 2
	if count != 2 {
		t.Fatal("member count")
	}
	for i := 0; i < count; i++ {
		at += 4
		for j := 0; j < 2; j++ {
			_, at, ok = readCurrentRawDstr(b, at)
			if !ok {
				t.Fatal("row strings")
			}
		}
		if b[at+4] != 1 {
			t.Fatal("roster channel lookup lacks advertised directory")
		}
		at += 2 + 8 + 4 + 4 + 1 // matched native scalar fields before third DSTR
		_, at, ok = readCurrentRawDstr(b, at)
		if !ok {
			t.Fatal("row auxiliary string")
		}
		if b[at+1] != 0 {
			t.Fatal("roster social identity changed to directory")
		}
		at += 4 // offline flag, region, optional grouping, representative
	}
	if at+4 != len(b) || binary.LittleEndian.Uint32(b[at:]) != 0 {
		t.Fatalf("native roster cursor=%d length=%d", at, len(b))
	}
}
