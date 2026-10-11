package raid

import (
	"strings"
	"testing"
)

func TestRealPVFFiendWarCompilesCompleteBattlefieldProgram(t *testing.T) {
	a := realFiendWarSource(t)
	p, err := LoadFiendWarProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.rules) != 141 {
		t.Fatal("actual Fiend War rule count changed", len(p.rules))
	}
	var init, timer, final bool
	for _, r := range p.rules {
		if len(r.conditions) == 1 && r.conditions[0].kind == "IF" && r.conditions[0].symbol == "FIENDWAR INIT" && r.conditions[0].value == 0 {
			var enemies, merchants int
			for _, c := range r.actions {
				if c.kind == "CREATE ENEMY" {
					enemies++
				}
				if c.kind == "CREATE MERCHANT" {
					merchants++
				}
			}
			init = enemies == 3 && merchants == 1
		}
		for _, c := range r.actions {
			if c.kind == "SET TIMER" && c.text == "move_enemy" && c.id == 7 && c.value == 210 {
				timer = true
			}
			if c.kind == "CLEAR PHASE" {
				final = len(r.conditions) == 2 && r.conditions[0].symbol == "TOP BOSS STATE" && r.conditions[0].value == 10 && r.conditions[1].symbol == "BOT BOSS STATE" && r.conditions[1].value == 10
			}
		}
	}
	if !init || !timer || !final {
		t.Fatal("initial enemies, delayed movement or double boss completion lost", init, timer, final)
	}
}

func TestRealPVFFiendWarProgramRejectsInvalidRuleDependencies(t *testing.T) {
	a := realFiendWarSource(t)
	const ref = "etc/raid/fiendwar/fiendwar.etc"
	text, err := a.ReadText(ref)
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := a.ReadText(ref + ".symbol")
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]string{
		"foreign-event-area":    strings.Replace(text, "[ON CHANGE AREA STATE]\n7 ", "[ON CHANGE AREA STATE]\n999 ", 1),
		"foreign-enemy-target":  strings.Replace(text, "[MOVE ENEMY]\n7 4 ", "[MOVE ENEMY]\n7 999 ", 1),
		"zero-timer":            strings.Replace(text, "`move_enemy` 7 150 ", "`move_enemy` 7 0 ", 1),
		"unknown-string-state":  strings.Replace(text, "[CHECK AREA STATE]\n7 `enemy` ", "[CHECK AREA STATE]\n7 `invented` ", 1),
		"unknown-symbol":        strings.Replace(text, "[FIENDWAR INIT]\n0 ", "[UNKNOWN INIT]\n0 ", 1),
		"unknown-directive":     strings.Replace(text, "[CREATE MERCHANT]", "[UNKNOWN MERCHANT]", 1),
		"missing-rule-boundary": strings.Replace(text, "[/BEHAVIOR]", "[/UNKNOWN BEHAVIOR]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if changed == text {
				t.Fatal("mutation did not match actual PVF")
			}
			if _, err := LoadFiendWarProgram(admissionSource{ref: changed, ref + ".symbol": symbols}); err == nil {
				t.Fatal("invalid battlefield rule accepted")
			}
		})
	}
}
