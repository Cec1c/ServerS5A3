package experienceitem

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
)

func parseCapsuleTestDocument(t *testing.T, text string) *pvf.Document {
	t.Helper()
	doc, err := pvf.Parse("stackable/test/generic_capsule.stk", text)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func capsuleFixture(effect, extra string) string {
	return "[name]\n`ignored display name`\n" +
		"[stackable type]\n`[etc]` 0\n" +
		"[usable job]\n`[all]`\n[/usable job]\n" +
		"[increase status type]\n" + effect + "\n[/increase status type]\n" +
		extra
}

func TestParseCapsuleFixedDefaultsAndRestrictions(t *testing.T) {
	doc := parseCapsuleTestDocument(t, capsuleFixture(
		"`[expUp]` 1234",
		"[minimum level]\n0\n"+
			"[maximum level]\n200\n"+
			"[impossible jobs]\n`at swordman` `thief`\n[/impossible jobs]\n"+
			"[action usable place]\n`[village]` `village` `[seria room]`\n[/action usable place]\n"+
			"[usable first growtype]\n3 3 4 5\n"+
			"[cool time]\n4321\n"+
			"[impossible contents]\n`gift` `hardcore mode`\n[/impossible contents]\n"+
			"[impossible contents]\n`hardcore mode` `ban redeemitem` `charac cargo`\n[/impossible contents]\n"+
			"[expiration date]\n`2030-01-01 00:00:00`\n"+
			"[usable period]\n1\n"+
			"[daily delete item]\n1\n"+
			"[usable expired item]\n1\n",
	))

	capsule, found, err := ParseCapsule(424242, doc)
	if err != nil || !found {
		t.Fatalf("ParseCapsule found=%t err=%v", found, err)
	}
	if capsule.ItemID != 424242 || capsule.PVFPath != doc.Path || capsule.Kind != CapsuleFixed || capsule.Amount != 1234 {
		t.Fatalf("capsule identity/effect = %+v", capsule)
	}
	if capsule.MinimumLevel != 1 || capsule.MaximumLevel != 200 || capsule.CoolTimeMS != 4321 {
		t.Fatalf("capsule bounds = %+v", capsule)
	}
	if !reflect.DeepEqual(capsule.Jobs, []string{"[all]"}) ||
		!reflect.DeepEqual(capsule.ExcludedJobs, []string{"[at swordman]", "[thief]"}) ||
		!reflect.DeepEqual(capsule.Places, []string{"[village]", "[village]", "[seria room]"}) ||
		!reflect.DeepEqual(capsule.FirstGrowTypes, []int{3, 4, 5}) ||
		!capsule.DisallowHardcore ||
		len(capsule.Bands) != 0 {
		t.Fatalf("capsule restrictions = %+v", capsule)
	}
}

func TestParseCapsulePercentVariants(t *testing.T) {
	for _, tag := range []string{"expPercentUp", "expUpByPercent"} {
		t.Run(tag, func(t *testing.T) {
			doc := parseCapsuleTestDocument(t, capsuleFixture("`["+tag+"]` 100", ""))
			capsule, found, err := ParseCapsule(7, doc)
			if err != nil || !found {
				t.Fatalf("found=%t err=%v", found, err)
			}
			if capsule.Kind != CapsulePercent || capsule.Amount != 100 {
				t.Fatalf("capsule = %+v", capsule)
			}
		})
	}
}

func TestParseCapsuleETCOneKeepsExplicitExperienceEffect(t *testing.T) {
	doc := parseCapsuleTestDocument(t, strings.Replace(capsuleFixture("`[expUpByPercent]` 30", ""), "`[etc]` 0", "`[etc]` 1", 1))
	d, found, err := ParseCapsule(10302223, doc)
	if err != nil || !found || d.Kind != CapsulePercent || d.Amount != 30 {
		t.Fatalf("definition=%+v found=%v err=%v", d, found, err)
	}
}

func TestParseCapsuleTable(t *testing.T) {
	doc := parseCapsuleTestDocument(t, capsuleFixture(
		"`[expUpByTable]` 1 10 100 11 20 4294967295",
		"",
	))
	capsule, found, err := ParseCapsule(8, doc)
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	want := []CapsuleBand{
		{Minimum: 1, Maximum: 10, Amount: 100},
		{Minimum: 11, Maximum: 20, Amount: ^uint32(0)},
	}
	if capsule.Kind != CapsuleTable || capsule.Amount != 0 || !reflect.DeepEqual(capsule.Bands, want) {
		t.Fatalf("capsule = %+v", capsule)
	}
}

func TestParseCapsuleCrackAndFixedVariant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effect string
		kind   CapsuleKind
		amount uint32
	}{
		{"empty", "`[expUpByCrackOfDimension]`", CapsuleCrack, 0},
		{"zero", "`[expUpByCrackOfDimension]` 0", CapsuleCrack, 0},
		{"fixed", "`[expUpByCrackOfDimension]` 90", CapsuleFixed, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseCapsuleTestDocument(t, capsuleFixture(tc.effect, ""))
			capsule, found, err := ParseCapsule(9, doc)
			if err != nil || !found {
				t.Fatalf("found=%t err=%v", found, err)
			}
			if capsule.Kind != tc.kind || capsule.Amount != tc.amount {
				t.Fatalf("capsule = %+v", capsule)
			}
		})
	}
}

func TestParseCapsuleRejectsDuplicateKeySections(t *testing.T) {
	for _, text := range []string{
		"[stackable type]\n`[etc]` 0\n" +
			"[usable job]\n`[all]`\n[/usable job]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n",
		"[stackable type]\n`[etc]` 0\n" +
			"[stackable type]\n`[etc]` 0\n" +
			"[usable job]\n`[all]`\n[/usable job]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n",
		capsuleFixture(
			"`[expUp]` 1",
			"[usable job]\n`[all]`\n[/usable job]\n",
		),
	} {
		doc := parseCapsuleTestDocument(t, text)
		if _, found, err := ParseCapsule(10, doc); !found || !errors.Is(err, ErrCapsule) {
			t.Fatalf("found=%t err=%v", found, err)
		}
	}
}

func TestParseCapsuleRejectsBadDefinitions(t *testing.T) {
	tests := map[string]string{
		"percent zero":  capsuleFixture("`[expPercentUp]` 0", ""),
		"percent high":  capsuleFixture("`[expUpByPercent]` 101", ""),
		"fixed zero":    capsuleFixture("`[expUp]` 0", ""),
		"fixed extra":   capsuleFixture("`[expUp]` 1 2", ""),
		"table overlap": capsuleFixture("`[expUpByTable]` 1 10 100 10 20 200", ""),
		"table order":   capsuleFixture("`[expUpByTable]` 11 20 100 1 10 200", ""),
		"table amount":  capsuleFixture("`[expUpByTable]` 1 10 0", ""),
		"table range":   capsuleFixture("`[expUpByTable]` 0 10 1", ""),
		"crack negative": capsuleFixture(
			"`[expUpByCrackOfDimension]` -1",
			"",
		),
		"bad minimum": capsuleFixture(
			"`[expUp]` 1",
			"[minimum level]\n256\n",
		),
		"bad maximum": capsuleFixture(
			"`[expUp]` 1",
			"[maximum level]\n-1\n",
		),
		"empty jobs": "[stackable type]\n`[etc]` 0\n" +
			"[usable job]\n[/usable job]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n",
		"unknown job": capsuleFixture(
			"`[expUp]` 1",
			"[impossible jobs]\n`unknown class`\n",
		),
		"bad place": capsuleFixture(
			"`[expUp]` 1",
			"[action usable place]\n`[dungeon]`\n",
		),
		"bad grow": capsuleFixture(
			"`[expUp]` 1",
			"[usable first growtype]\n16\n",
		),
		"bad cooltime": capsuleFixture(
			"`[expUp]` 1",
			"[cool time]\n-1\n",
		),
		"unknown content": capsuleFixture(
			"`[expUp]` 1",
			"[impossible contents]\n`cash only`\n",
		),
		"unknown behavior": capsuleFixture(
			"`[expUp]` 1",
			"[action type]\n`[booster]`\n[/action type]\n",
		),
		"unknown use count": capsuleFixture(
			"`[expUp]` 1",
			"[total usable count]\n1\n",
		),
		"unknown cooltime group": capsuleFixture(
			"`[expUp]` 1",
			"[cooltime group]\n77\n",
		),
		"wrong stackable": "[stackable type]\n`[waste]` 0\n" +
			"[usable job]\n`[all]`\n[/usable job]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n",
		"unverified etc variant": "[stackable type]\n`[etc]` 8\n" +
			"[usable job]\n`[all]`\n[/usable job]\n" +
			"[increase status type]\n`[expUp]` 1\n[/increase status type]\n",
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			doc := parseCapsuleTestDocument(t, text)
			if _, found, err := ParseCapsule(11, doc); !found || !errors.Is(err, ErrCapsule) {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
	}
}

func TestParseCapsuleLeavesSkillBooksToLegacyHandler(t *testing.T) {
	doc := parseCapsuleTestDocument(t, "[increase status type]\n`[skillUp]` 5\n[/increase status type]\n")
	capsule, found, err := ParseCapsule(12, doc)
	if err != nil || found || !reflect.DeepEqual(capsule, Capsule{}) {
		t.Fatalf("capsule=%+v found=%t err=%v", capsule, found, err)
	}
}

func TestParseCapsuleIgnoresNestedStatusEffect(t *testing.T) {
	doc := parseCapsuleTestDocument(t,
		"[item category]\n"+
			"[increase status type]\n`[expUp]` 5\n[/increase status type]\n"+
			"[/item category]\n",
	)
	capsule, found, err := ParseCapsule(14, doc)
	if err != nil || found || !reflect.DeepEqual(capsule, Capsule{}) {
		t.Fatalf("capsule=%+v found=%t err=%v", capsule, found, err)
	}
}

func TestCapsuleValidateRejectsDirectBadDefinitions(t *testing.T) {
	valid := Capsule{
		ItemID:       13,
		PVFPath:      "stackable/test/generic_capsule.stk",
		Kind:         CapsuleFixed,
		Amount:       1,
		MinimumLevel: 1,
		MaximumLevel: 255,
		Jobs:         []string{"[all]"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid capsule: %v", err)
	}

	tests := map[string]Capsule{
		"zero id": {
			PVFPath: valid.PVFPath, Kind: CapsuleFixed, Amount: 1,
			MinimumLevel: 1, MaximumLevel: 255, Jobs: []string{"[all]"},
		},
		"unbracketed job": func() Capsule {
			value := valid
			value.Jobs = []string{"all"}
			return value
		}(),
		"duplicate grow": func() Capsule {
			value := valid
			value.FirstGrowTypes = []int{3, 3}
			return value
		}(),
		"bad level": func() Capsule {
			value := valid
			value.MinimumLevel = 0
			return value
		}(),
		"bad table": func() Capsule {
			value := valid
			value.Kind = CapsuleTable
			value.Amount = 0
			value.Bands = []CapsuleBand{
				{Minimum: 1, Maximum: 10, Amount: 1},
				{Minimum: 10, Maximum: 20, Amount: 2},
			}
			return value
		}(),
	}
	for name, capsule := range tests {
		t.Run(name, func(t *testing.T) {
			if err := capsule.Validate(); !errors.Is(err, ErrCapsule) {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
}

// TestParseCapsuleAcceptsTradeOnlySections 守住「纯交易字段不影响胶囊可用性」。
// [price] 是卖店价、[need material] 是 NPC 兑换价，两者都不参与胶囊行为。
func TestParseCapsuleAcceptsTradeOnlySections(t *testing.T) {
	doc := parseCapsuleTestDocument(t, capsuleFixture(
		"`[expPercentUp]` 50",
		"[price]\n500000\n"+
			"[need material]\n10003231 1\n",
	))
	capsule, found, err := ParseCapsule(10005308, doc)
	if err != nil || !found {
		t.Fatalf("ParseCapsule found=%t err=%v", found, err)
	}
	if capsule.Kind != CapsulePercent || capsule.Amount != 50 {
		t.Fatalf("capsule effect = %+v", capsule)
	}
}
