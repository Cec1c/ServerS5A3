package fiendwar

import (
	"os"
	"reflect"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Actual legacy 1528 now supplies the mechanism handshake. An unfinished shared
// pool cannot be cleared by the old missing-handshake fallback.
func TestFiendWarClientBossClearRejectsAMissingMechanismHandshake(t *testing.T) {
	b := newRealFiendWarBossBattle(t)
	// The two mechanism maps the client DID walk into (04:53:47 / 04:54:25 live), so
	// this is the exact state the live run was in when the bosses died.
	enterFiendWarMechanismMaps(t, b)

	// 两队各拿一颗**不同**的球（5=宝石/虚无 6=火 7=冰 8=风），球池仍是满的。
	for _, side := range []string{"top boss", "bot boss"} {
		if got := b.Value(side + " state"); got < 5 || got > 8 {
			t.Fatalf("%s state after boss pattern init = %d, want 5..8", side, got)
		}
	}
	if b.Value("top boss state") == b.Value("bot boss state") {
		t.Fatalf("两侧拿到了同一颗球：top=%d bot=%d", b.Value("top boss state"), b.Value("bot boss state"))
	}
	// The live packet, verbatim: `01 e7 05 … 00000000 64000000` = kind 0 value 100.
	// It used to be rejected ("invalid fiendwar boss HP") because the state was not
	// yet in 5..9; the client's own HUD value must never be refused.
	if err := b.BossReport(4978, 0, 100, time.Unix(0, 0)); err != nil {
		t.Fatalf("boss HP report rejected at state 5: %v", err)
	}

	before := b.Clone()
	for _, dungeon := range []int{4978, 4953} {
		if err := b.ClearClientBossRoom(dungeon, time.Unix(0, 0)); err == nil {
			t.Fatal("death bypassed the shared mechanism pool")
		}
	}
	if !reflect.DeepEqual(before.Symbols, b.Symbols) || !reflect.DeepEqual(before.Areas, b.Areas) || b.Complete {
		t.Fatal("rejected death changed battle progress")
	}
}

// Pool zero at entry is not an open damage window: the local side must be 9.
func TestFiendWarClientBossClearRejectsTheEntryState(t *testing.T) {
	b := newRealFiendWarBossBattle(t)
	if got := b.Value("top boss state"); got != 2 {
		t.Fatalf("top boss state on entry = %d, want 2", got)
	}
	if err := b.BossReport(4978, 0, 100, time.Unix(0, 0)); err != nil {
		t.Fatalf("boss HP report rejected at state 2: %v", err)
	}
	if err := b.Clear(4978, time.Unix(0, 0)); err == nil {
		t.Fatal("the strict Clear accepted a boss room whose mechanisms never ran")
	}
	if err := b.ClearClientBossRoom(4978, time.Unix(0, 0)); err == nil {
		t.Fatal("entry-state death bypassed the mechanisms")
	}
	if got := b.Value("top boss state"); got != 2 {
		t.Fatalf("rejected death changed entry state to %d", got)
	}
}

// A mechanism report the client really does send (kind 1, value == state) keeps
// working exactly as before, and a clear that follows it must not re-derive the
// state.
func TestFiendWarClientBossClearLeavesADrivenMechanismAlone(t *testing.T) {
	b := newRealFiendWarBossBattle(t)
	enterFiendWarMechanismMaps(t, b)
	now := time.Unix(0, 0)
	// 每一侧打掉**自己手上那颗**（value == 自己这一侧的 boss state），四颗清完。
	for _, room := range []struct {
		dungeon int
		side    string
	}{{4978, "top boss"}, {4953, "bot boss"}, {4978, "top boss"}, {4953, "bot boss"}} {
		state := b.Value(room.side + " state")
		if state < 5 || state > 8 {
			t.Fatalf("%s 没有球可打：state=%d", room.side, state)
		}
		if err := b.BossReport(room.dungeon, 1, state, now); err != nil {
			t.Fatalf("mechanism %s state %d: %v", room.side, state, err)
		}
	}
	if got := b.Value("boss pattern state"); got != 0 {
		t.Fatalf("四颗球清完后机制条 = %d，want 0", got)
	}
	if got := b.Value("top boss state"); got != 9 {
		t.Fatalf("mechanism sequence left top boss state at %d, want 9", got)
	}
	if err := b.BossReport(4978, 0, 0, now); err != nil {
		t.Fatalf("damage-window HP report: %v", err)
	}
	if err := b.ClearClientBossRoom(4978, now); err != nil {
		t.Fatalf("client boss clear refused: %v", err)
	}
	if got := b.Value("boss pattern state"); got != 0 {
		t.Fatalf("a driven clear overwrote the live mechanism symbols: %d", got)
	}
}

// The per-side HP guard: one boss at zero HP no longer blocks the other side's
// clear, and the strict Clear still refuses the side whose mechanism is unfinished.
func TestFiendWarBossClearIsDecidedPerSide(t *testing.T) {
	now := time.Unix(0, 0)
	config := &FiendWarBattleConfig{
		Areas: map[int]FiendWarArea{
			30: {Index: 30, Dungeon: 4978, Kind: "top boss"},
			31: {Index: 31, Dungeon: 4953, Kind: "bot boss"},
		},
		Symbols: map[string]uint32{
			"top boss state": 1, "bot boss state": 2,
			"top boss hp": 3, "bot boss hp": 4,
			"top boss view hp": 5, "bot boss view hp": 6,
			"boss pattern state": 7,
		},
	}
	b := &FiendWarBattle{
		Config:        config,
		Symbols:       map[uint32]int{1: 9, 2: 5, 3: 0, 4: 100, 5: 0, 6: 100},
		Areas:         map[int]FiendWarAreaState{30: {Group: 1}, 31: {Group: 2}},
		DungeonStates: map[int]string{},
		Timers:        map[FiendWarTimerKey]FiendWarTimer{},
		GroupAreas:    map[byte]int{},
		MemberAreas:   map[uint16]int{},
	}
	if err := b.Clear(4978, now); err != nil {
		t.Fatalf("top boss clear blocked by the other side's HP: %v", err)
	}
	if err := b.Clear(4953, now); err == nil {
		t.Fatal("bot boss clear accepted while its mechanism was unfinished")
	}
	if err := b.ClearClientBossRoom(4953, now); err == nil {
		t.Fatal("client death bypassed the unfinished side")
	}
	b.set("bot boss state", 9)
	if err := b.ClearClientBossRoom(4953, now); err != nil {
		t.Fatalf("valid window with stale HP refused: %v", err)
	}
}

func newRealFiendWarBossBattle(t *testing.T) *FiendWarBattle {
	t.Helper()
	path := os.Getenv("DNF_FIENDWAR_REAL_PVF")
	if path == "" {
		t.Skip("DNF_FIENDWAR_REAL_PVF not set")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadFiendWarConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFiendWarBattle(c.Battle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	if err := b.Apply(FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	// Clearing the three 兵站本营 by hand is a ten-dungeon walk; the objective is
	// only the state it leaves behind ([IF … COMMAND CENTER STATE] 0 → both boss
	// rooms open), so drive the symbols the same way the retreat subtest does.
	for _, name := range []string{"top command center state", "mid command center state", "bot command center state"} {
		b.set(name, 0)
	}
	if err := b.Apply(FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	b.GroupAreas[1], b.GroupAreas[2] = 30, 31
	for i, dungeon := range []int{4978, 4953} {
		if err := b.Enter(byte(i+1), dungeon, now); err != nil {
			t.Fatalf("enter boss room %d: %v", dungeon, err)
		}
	}
	return b
}

func enterFiendWarMechanismMaps(t *testing.T, b *FiendWarBattle) {
	t.Helper()
	now := time.Unix(0, 0)
	for _, mapID := range []int{306000463, 306000460} {
		if err := b.Apply(FiendWarEvent{Kind: "on enter map", ID: mapID}, now); err != nil {
			t.Fatalf("on enter map %d: %v", mapID, err)
		}
	}
}
