package equipmentservice

import (
	"errors"
	"testing"

	pvf "longheng.io/server/internal/modules/dnf/pvf"
)

type privateTransfer008Source map[string]string

func (s privateTransfer008Source) ReadText(p string) (string, error) {
	if text, found := s[p]; found {
		return text, nil
	}
	return "", pvf.ErrDocNotFound
}

func TestPrivateTransfer008SeparateNativeTable(t *testing.T) {
	native := "[stone type]\n7\n[/stone type]\n[stone type]\n8\n[/stone type]\n"
	for _, tc := range []struct {
		name, private   string
		missing, reject bool
	}{
		{name: "absent keeps native", missing: true},
		{name: "fixed100", private: "[private epic piece fixed cost]\n100\n[/private epic piece fixed cost]"},
		{name: "zero", private: "[private epic piece fixed cost]\n0\n[/private epic piece fixed cost]", reject: true},
		{name: "negative", private: "[private epic piece fixed cost]\n-1\n[/private epic piece fixed cost]", reject: true},
		{name: "overflow", private: "[private epic piece fixed cost]\n4294967296\n[/private epic piece fixed cost]", reject: true},
		{name: "empty", reject: true},
		{name: "unclosed", private: "[private epic piece fixed cost]\n100", reject: true},
		{name: "extra", private: "[private epic piece fixed cost]\n100 200\n[/private epic piece fixed cost]", reject: true},
		{name: "string", private: "[private epic piece fixed cost]\n`100`\n[/private epic piece fixed cost]", reject: true},
		{name: "unknown", private: "[other]\n100\n[/other]", reject: true},
		{name: "duplicate", private: "[private epic piece fixed cost]\n100\n[/private epic piece fixed cost]\n[private epic piece fixed cost]\n100\n[/private epic piece fixed cost]", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := privateTransfer008Source{TransferConfigPath: native}
			if !tc.missing {
				s[PrivateTransferConfigPath] = tc.private
			}
			c, err := LoadTransferConfig(s)
			if tc.reject {
				if !errors.Is(err, ErrRules) {
					t.Fatalf("expected invalid rules, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := int64(100)
			if tc.missing {
				want = 0
			}
			if c.Rules[7].EpicPieceFixedCost != want || c.Rules[8].EpicPieceFixedCost != 0 || len(c.Rules) != 2 {
				t.Fatal(c)
			}
		})
	}
	s := privateTransfer008Source{TransferConfigPath: native, PrivateTransferConfigPath: "[private epic piece fixed cost]\n100\n[/private epic piece fixed cost]"}
	s[TransferConfigPath] = "[stone type]\n8\n[/stone type]"
	if _, err := LoadTransferConfig(s); !errors.Is(err, ErrRules) {
		t.Fatalf("missing Grandi rule: %v", err)
	}
	s[TransferConfigPath] = "[stone type]\n7\n[private epic piece fixed cost]\n100\n[/private epic piece fixed cost]\n[/stone type]"
	if _, err := LoadTransferConfig(s); !errors.Is(err, ErrRules) {
		t.Fatalf("duplicate legacy cost: %v", err)
	}
}
