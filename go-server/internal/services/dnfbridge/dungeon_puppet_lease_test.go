package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 现场形状（2026-11-09 幽暗密林 / 雷鸣废墟）：owner=ffff、一条 14 字节贡献、贡献值非零。
// 人偶本体 kind=0x111（键固定），人偶召唤的机器人 kind=0x211（每台一个键）。
func puppetAllyDeathBodyKind(target uint32, kind uint16, key uint16) []byte {
	body := ekernDiagnosticBody(target, ^uint16(0), 0)
	body = append(body, make([]byte, dungeoncmd.Official249DieMonsterEntrySize)...)
	body[30] = 1
	entry := body[31:]
	binary.LittleEndian.PutUint16(entry, kind)
	binary.LittleEndian.PutUint16(entry[2:], key)
	binary.LittleEndian.PutUint64(entry[4:], 1)
	return body
}

func puppetAllyDeathBody(target uint32, key uint16) []byte {
	return puppetAllyDeathBodyKind(target, currentDungeonPuppetContributionKindAICharacter, key)
}

func puppetLeaseFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState) {
	t.Helper()
	s, p, _ := ekernDiagnosticFixture(t, false)
	rt := p.dungeon.runtime
	rt.Dungeon.ID = 3
	rt.Dungeon.Path = "dungeon/ordinary/synthetic.dgn"
	return s, p, rt
}

func armPuppetLease(p *gameSession, expiry time.Time) {
	p.puppetLease = &currentDungeonPuppetLease{
		CharacterID: p.selectedCharacterID, ItemID: 2600264, APC: 25301, ExpiresAt: expiry,
	}
}

// 没有消耗凭证：保持今天的严格拒绝，不发 op38。
func TestPuppetAllyDeathWithoutProvenSourceRejected(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	conn := p.conn.(*bufferConn)
	target := rt.Room.Snapshot().Monsters[0].ObjectKey
	if err := s.handleDungeonMonsterDeath(p, puppetAllyDeathBody(target, 2)); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("unproven report produced output: %x", conn.write.Bytes())
	}
	if _, live := rt.Room.AnnouncedMonster(target); !live {
		t.Fatal("unproven report defeated the target")
	}
}

// 有租约：人偶本体（0x111）与它召唤的机器人（0x211，各自不同键）都能结算。
func TestPuppetAllyDeathLeaseAdmitsBodyAndSummons(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	armPuppetLease(p, time.Now().Add(time.Minute))
	monsters := rt.Room.Snapshot().Monsters
	conn := p.conn.(*bufferConn)

	cases := []struct {
		monster int
		kind    uint16
		key     uint16
	}{
		{0, currentDungeonPuppetContributionKindAICharacter, 2},
		{1, currentDungeonPuppetContributionKindMonster, 18},
	}
	for _, testCase := range cases {
		conn.write.Reset()
		body := puppetAllyDeathBodyKind(monsters[testCase.monster].ObjectKey, testCase.kind, testCase.key)
		if err := s.handleDungeonMonsterDeath(p, body); err != nil {
			t.Fatal(err)
		}
		ack := abyssOp38Bodies(t, conn.write.Bytes())
		if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != monsters[testCase.monster].ObjectKey {
			t.Fatalf("kind %#x key %d not notified: %x", testCase.kind, testCase.key, ack)
		}
		if _, defeated := rt.Room.DefeatedDeathTarget(monsters[testCase.monster].ObjectKey); !defeated {
			t.Fatalf("kind %#x key %d not defeated", testCase.kind, testCase.key)
		}
	}
}

// 租约过期：回到严格拒绝。
func TestPuppetAllyDeathExpiredLeaseRejected(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	armPuppetLease(p, time.Now().Add(-time.Minute))
	conn := p.conn.(*bufferConn)
	target := rt.Room.Snapshot().Monsters[0].ObjectKey
	if err := s.handleDungeonMonsterDeath(p, puppetAllyDeathBody(target, 2)); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("expired lease produced output: %x", conn.write.Bytes())
	}
	if p.puppetLease != nil {
		t.Fatal("expired lease was not dropped")
	}
}

// 贡献键冒用本房已宣告 actor：不走租约，继续拒绝。
func TestPuppetAllyDeathRejectsAnnouncedKey(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	armPuppetLease(p, time.Now().Add(time.Minute))
	monsters := rt.Room.Snapshot().Monsters
	conn := p.conn.(*bufferConn)
	body := puppetAllyDeathBody(monsters[0].ObjectKey, uint16(monsters[1].ObjectKey))
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("announced actor key was admitted: %x", conn.write.Bytes())
	}
}

// 目标完全不在册（地图 act 在客户端本地召唤的怪）：有租约时只回发死亡通知，
// 不改服务端状态、不结算。没有租约时什么都不发。
func TestPuppetAllyDeathClientOnlyTargetNotifiesWithoutSettling(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	conn := p.conn.(*bufferConn)
	if err := s.handleDungeonMonsterDeath(p, puppetAllyDeathBody(65000, 2)); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("client-only target notified without a lease: %x", conn.write.Bytes())
	}

	armPuppetLease(p, time.Now().Add(time.Minute))
	conn.write.Reset()
	if err := s.handleDungeonMonsterDeath(p, puppetAllyDeathBody(65000, 2)); err != nil {
		t.Fatal(err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != 65000 {
		t.Fatalf("client-only target was not notified: %x", ack)
	}
	if _, defeated := rt.Room.DefeatedDeathTarget(65000); defeated {
		t.Fatal("client-only target was settled server-side")
	}
	if monsters := rt.Room.Snapshot().Monsters; len(monsters) == 0 {
		t.Fatal("room state lost")
	}
}

// 每房一组键：同房允许多个召唤物键、换房重置、数量有上限。
func TestPuppetLeaseAdmitsPerRoomKeys(t *testing.T) {
	lease := &currentDungeonPuppetLease{CharacterID: 99, ExpiresAt: time.Now().Add(time.Minute)}
	if !currentDungeonPuppetLeaseAdmits(lease, "0:1", 2) {
		t.Fatal("body key rejected")
	}
	if !currentDungeonPuppetLeaseAdmits(lease, "0:1", 18) {
		t.Fatal("summon key rejected in the same room")
	}
	if !currentDungeonPuppetLeaseAdmits(lease, "0:1", 18) {
		t.Fatal("repeat of a known summon key rejected")
	}
	if !currentDungeonPuppetLeaseAdmits(lease, "2:1", 35) {
		t.Fatal("key was not accepted after a room change")
	}
	if len(lease.Keys) != 1 {
		t.Fatalf("room change did not reset the key set: %d", len(lease.Keys))
	}
	for index := 0; index < currentDungeonPuppetLeaseKeysPerRoom-1; index++ {
		if !currentDungeonPuppetLeaseAdmits(lease, "2:1", uint32(100+index)) {
			t.Fatalf("key %d rejected before the cap", 100+index)
		}
	}
	if currentDungeonPuppetLeaseAdmits(lease, "2:1", 9999) {
		t.Fatal("key admitted past the per-room cap")
	}
	if currentDungeonPuppetLeaseAdmits(nil, "0:1", 2) {
		t.Fatal("nil lease admitted")
	}
}

// 本图 PVF 声明了非敌对 AI 角色时，仍不参与放行判定（只用于日志）。
func TestSceneDeclaresFriendlyAICharacter(t *testing.T) {
	if currentDungeonSceneDeclaresFriendlyAICharacter(worldmap.DungeonRoomScene{}) {
		t.Fatal("empty scene declared a friendly AI character")
	}
	hostile := worldmap.DungeonRoomScene{AICharacters: []worldmap.AICharacter{{Code: 1, Faction: "monster"}}}
	if currentDungeonSceneDeclaresFriendlyAICharacter(hostile) {
		t.Fatal("hostile AI character counted as friendly")
	}
	friendly := worldmap.DungeonRoomScene{AICharacters: []worldmap.AICharacter{{Code: 1, Faction: "friendly"}}}
	if !currentDungeonSceneDeclaresFriendlyAICharacter(friendly) {
		t.Fatal("friendly AI character not recognized")
	}
}

// Boss 被人偶打死：必须结算，否则客户端留一只白 Boss、本局作废。
func TestPuppetAllyDeathBossTargetSettles(t *testing.T) {
	source := bridgeGenericDungeonMovePVF(false)
	source["map/dungeon/test/start.map"] += "[monster]\n" +
		"3001 10 0 100 200 0 0 0 `[fixed]` `[boss]`\n[/monster]\n"
	s, rt := prepareSyntheticMoveRuntimeFromPVF(t, source)
	if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
		t.Fatal(err)
	}
	s.options.currentClientVersion = "official-24.9"
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99,
		connID: "PRIVATE_CONNECTION", accountID: "PRIVATE_ACCOUNT",
		dungeon: dungeonSessionState{runtime: rt}}
	armPuppetLease(p, time.Now().Add(time.Minute))
	conn := p.conn.(*bufferConn)
	target := rt.Room.Snapshot().Monsters[0].ObjectKey
	if err := s.handleDungeonMonsterDeath(p, puppetAllyDeathBody(target, 2)); err != nil {
		t.Fatalf("boss lease death errored: %v", err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) == 0 {
		t.Fatal("boss death produced no notification")
	}
	if _, defeated := rt.Room.DefeatedDeathTarget(target); !defeated {
		t.Fatal("boss target was not defeated; run would brick")
	}
}

// 同房两只在册怪互杀：不需要人偶租约；贡献者不在册、或贡献者就是目标时拒绝。
func TestRoomFactionDeathSettlesAnnouncedMonsterKills(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	monsters := rt.Room.Snapshot().Monsters
	conn := p.conn.(*bufferConn)
	body := puppetAllyDeathBodyKind(monsters[1].ObjectKey, currentDungeonPuppetContributionKindMonster, uint16(monsters[0].ObjectKey))
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != monsters[1].ObjectKey {
		t.Fatalf("announced monster kill was not notified: %x", ack)
	}
	if _, defeated := rt.Room.DefeatedDeathTarget(monsters[1].ObjectKey); !defeated {
		t.Fatal("announced monster kill was not settled")
	}

	unknown, _, unknownRT := puppetLeaseFixture(t)
	unknownMonsters := unknownRT.Room.Snapshot().Monsters
	scene, _ := unknownRT.Session.Scene()
	req, err := unknown.decodeCurrentDungeonMonsterDeath(
		puppetAllyDeathBodyKind(unknownMonsters[1].ObjectKey, currentDungeonPuppetContributionKindMonster, 65000))
	if err != nil {
		t.Fatal(err)
	}
	if unknown.currentDungeonRoomFactionDeath(unknownRT, scene, req) {
		t.Fatal("unannounced contributor was admitted")
	}
}

// 贡献者已经死了（残留效果晚一步打死目标）也必须结算。
func TestRoomFactionDeathAcceptsAlreadyDefeatedContributor(t *testing.T) {
	s, p, rt := puppetLeaseFixture(t)
	monsters := rt.Room.Snapshot().Monsters
	conn := p.conn.(*bufferConn)
	first := ekernDiagnosticBody(monsters[0].ObjectKey, ^uint16(0), 0)
	if err := s.handleDungeonMonsterDeath(p, first); err != nil {
		t.Fatal(err)
	}
	if _, defeated := rt.Room.DefeatedDeathTarget(monsters[0].ObjectKey); !defeated {
		t.Fatal("contributor was not defeated first")
	}
	conn.write.Reset()
	body := puppetAllyDeathBodyKind(monsters[1].ObjectKey, currentDungeonPuppetContributionKindMonster, uint16(monsters[0].ObjectKey))
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != monsters[1].ObjectKey {
		t.Fatalf("defeated-contributor kill was not notified: %x", ack)
	}
}
