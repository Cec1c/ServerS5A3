package raid

import (
	"testing"
	"time"
)

func TestRealPVFFiendWarRuleClockMovementAndClone(t *testing.T) {
	p, err := LoadFiendWarProgram(realFiendWarSource(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1800000000, 0)
	s, commands, err := p.beginRules(start)
	if err != nil {
		t.Fatal(err)
	}
	var enemyAreas []uint32
	var merchants int
	for _, c := range commands {
		if c.kind == "CREATE ENEMY" {
			enemyAreas = append(enemyAreas, c.id)
		}
		if c.kind == "CREATE MERCHANT" {
			merchants++
		}
	}
	if len(enemyAreas) != 3 || enemyAreas[0] != 14 || enemyAreas[1] != 16 || enemyAreas[2] != 18 || merchants != 1 || s.symbols["FIENDWAR INIT"] != 1 || s.deadline.Sub(start) != time.Hour {
		t.Fatal("actual initial battlefield directives lost", commands)
	}
	// A battlefield owner supplies the validated area change. Commands remain
	// explicit until the enemy-generation/movement owner has executed them.
	n := s.clone()
	n.areas[7] = "enemy"
	if _, err := n.evaluate(start, []fiendWarEvent{{kind: "ON CHANGE AREA STATE", id: 7}}); err != nil {
		t.Fatal(err)
	}
	if !n.timers[fiendWarTimerKey{"warning_enemy", 7}].Equal(start.Add(145*time.Second)) || !n.timers[fiendWarTimerKey{"move_enemy", 7}].Equal(start.Add(150*time.Second)) {
		t.Fatal("actual warning and movement deadlines changed", n.timers)
	}
	if s.areas[7] == "enemy" || !s.last.Equal(start) || !s.timers[fiendWarTimerKey{"move_enemy", 7}].IsZero() {
		t.Fatal("clone mutated prior battlefield")
	}
	warn, commands, err := n.tick(start.Add(145 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var warnings, moves int
	for _, c := range commands {
		if c.kind == "WARNING ENEMY" {
			warnings++
		}
		if c.kind == "MOVE ENEMY" {
			moves++
		}
	}
	if warnings != 4 || moves != 0 {
		t.Fatal("warning must precede actual movement", commands)
	}
	moved, commands, err := warn.tick(start.Add(150 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].kind != "DELETE MONSTER" || commands[0].id != 7 || commands[1].kind != "MOVE ENEMY" || commands[1].id != 7 || commands[1].target != 4 {
		t.Fatal("actual enemy movement command order changed", commands)
	}
	_, commands, err = moved.tick(start.Add(151 * time.Second))
	if err != nil || len(commands) != 0 {
		t.Fatal("expired movement replayed", commands, err)
	}
	if _, _, err := moved.tick(start.Add(149 * time.Second)); err == nil {
		t.Fatal("clock rewind accepted")
	}
	failed, _, err := moved.tick(start.Add(time.Hour))
	if err != nil || !failed.failed || failed.cleared || len(failed.timers) != 0 {
		t.Fatal("phase deadline did not close without success", err)
	}
}

func TestRealPVFFiendWarDoubleBossRulesRequireCombatStateAndBothClears(t *testing.T) {
	p, err := LoadFiendWarProgram(realFiendWarSource(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1800000000, 0)
	s, _, err := p.beginRules(start)
	if err != nil {
		t.Fatal(err)
	}
	// Values 1/9 below are trusted combat fixtures, not native packet evidence.
	// No transport adapter accepts this arbitrary input from a player.
	s.symbols["TOP BOSS STATE"], s.symbols["BOT BOSS STATE"] = 1, 1
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON ENTER DUNGEON", id: 4978}}); err != nil {
		t.Fatal(err)
	}
	if s.symbols["TOP BOSS STATE"] != 2 || s.symbols["TOP BOSS HP"] != 100 {
		t.Fatal("top boss entry did not apply actual HP/state literals")
	}
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON ENTER DUNGEON", id: 4953}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON ENTER MAP", id: 306000463}}); err != nil {
		t.Fatal(err)
	}
	commands, err := s.evaluate(start, []fiendWarEvent{{kind: "ON ENTER MAP", id: 306000460}})
	if err != nil {
		t.Fatal(err)
	}
	var patterns int
	for _, c := range commands {
		if c.kind == "BOSS PATTERN INIT" {
			patterns++
		}
	}
	if patterns != 1 || s.symbols["TOP BOSS STATE"] != 4 || s.symbols["BOT BOSS STATE"] != 4 {
		t.Fatal("both physical map events required for boss pattern", commands)
	}
	s.dungeons[4978] = "clear"
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON CHANGE DUNGEON STATE", id: 4978}}); err != nil {
		t.Fatal(err)
	}
	if s.symbols["TOP BOSS STATE"] != 4 || s.cleared {
		t.Fatal("plain dungeon clear bypassed required boss combat state")
	}
	s.symbols["TOP BOSS STATE"] = 9
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON CHANGE DUNGEON STATE", id: 4978}}); err != nil {
		t.Fatal(err)
	}
	if s.symbols["TOP BOSS STATE"] != 10 || s.cleared {
		t.Fatal("one completed boss incorrectly completed the raid")
	}
	s.symbols["BOT BOSS STATE"], s.dungeons[4953] = 9, "clear"
	if _, err := s.evaluate(start, []fiendWarEvent{{kind: "ON CHANGE DUNGEON STATE", id: 4953}}); err != nil {
		t.Fatal(err)
	}
	if !s.cleared || s.failed || len(s.timers) != 0 {
		t.Fatal("both validated boss clears did not complete actual phase")
	}
}
