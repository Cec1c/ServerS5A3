package experiencebuff

import (
	"math"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestExperiencePotionRatesAndExpiry(t *testing.T) {
	now := time.Unix(1700000000, 123000000)
	for _, tc := range []struct {
		tag, rate, kind string
		bonus           uint32
	}{{"exp bonus rate", "1.00", Clear, 1000}, {"exp bonus rate", "2.00", Clear, 2000}, {"exp bonus rate", "4.00", Clear, 4000}, {"quest exp bonus rate", "0.10", Quest, 100}} {
		t.Run(tc.kind+tc.rate, func(t *testing.T) {
			doc, err := pvf.Parse("potion.stk", "["+tc.tag+"]\n"+tc.rate+"\n[stat change duration]\n1800000 `myself`\n[cool time]\n1800000\n")
			if err != nil {
				t.Fatal(err)
			}
			effect, err := Parse(doc)
			if err != nil {
				t.Fatal(err)
			}
			c := repo.CharacterRecord{Level: 90}
			if err := Record(&c, effect, 1, now); err != nil {
				t.Fatal(err)
			}
			// A detached persisted character, including after reconnect, owns the timer.
			c = repo.CloneCharacter(c)
			if Bonus(c, tc.kind, 1000, now.Add(-time.Millisecond)) != 0 {
				t.Fatal("effect applied before use")
			}
			if got := Bonus(c, tc.kind, 1000, now.Add(29*time.Minute)); got != tc.bonus {
				t.Fatalf("bonus=%d want %d", got, tc.bonus)
			}
			if got := Bonus(c, tc.kind, 1000, now.Add(30*time.Minute)); got != 0 {
				t.Fatalf("expired bonus=%d", got)
			}
			if err := Record(&c, effect, 2, now.Add(time.Second)); err != ErrCooldown {
				t.Fatalf("duplicate use=%v", err)
			}
			other := Clear
			if tc.kind == Clear {
				other = Quest
			}
			if Bonus(c, other, 1000, now) != 0 {
				t.Fatal("bonus crossed reward family")
			}
			if Bonus(c, tc.kind, math.MaxUint32, now) != 0 {
				t.Fatal("bonus overflowed")
			}
		})
	}
}

func TestExperiencePotionUsableJob(t *testing.T) {
	doc, err := pvf.Parse("priest.stk", "[usable job] `[priest]`\n[/usable job]\n[exp bonus rate] 1.00\n[stat change duration] 1800000\n")
	if err != nil {
		t.Fatal(err)
	}
	effect, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	c := repo.CharacterRecord{Job: "2", Level: 90}
	if Record(&c, effect, 1, time.Now()) != ErrEffect {
		t.Fatal("restricted potion accepted another job")
	}
	c.Job = "4"
	if err := Record(&c, effect, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestExperiencePotionMalformedPVF(t *testing.T) {
	for _, text := range []string{"[exp bonus rate]\n-1.0\n[stat change duration]\n1000", "[exp bonus rate]\n1.0", "[exp bonus rate]\n1.0 2.0\n[stat change duration]\n1000", "[exp bonus rate]\n1.0\n[quest exp bonus rate]\n0.1\n[stat change duration]\n1000"} {
		d, err := pvf.Parse("invalid.stk", text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(d); err == nil {
			t.Fatalf("accepted malformed effect: %s", text)
		}
	}
}
