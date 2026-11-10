package wedding

import (
	"fmt"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/repository"
)

type GuestGiftTier struct {
	Maximum int64
	ByKind  map[byte][]repository.MailAttachment
}

type GuestRules struct {
	MinimumGold, MaximumGold, Charge int64
	Capacity                         map[byte]int
	Gifts                            []GuestGiftTier
}

func LoadGuestRules(source pvf.Source) (*GuestRules, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	text, err := source.ReadText(SystemPath)
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse(SystemPath, text)
	if err != nil {
		return nil, err
	}
	limits, charge := doc.Ints("money gift limit"), doc.Ints("money gift charge")
	if len(limits) != 2 || limits[0] <= 0 || limits[1] < limits[0] || limits[1] > 0x7fffffff || len(charge) != 1 || charge[0] < 0 || charge[0] > 100 {
		return nil, ErrUnavailable
	}
	rules := &GuestRules{MinimumGold: limits[0], MaximumGold: limits[1], Charge: charge[0], Capacity: map[byte]int{}}
	rows := doc.Ints("ceremony")
	if len(rows) == 0 || len(rows)%4 != 0 {
		return nil, ErrUnavailable
	}
	for i := 0; i < len(rows); i += 4 {
		if rows[i] < 0 || rows[i] > 255 || rows[i+3] <= 0 || rows[i+3] > 127 {
			return nil, ErrUnavailable
		}
		rules.Capacity[byte(rows[i])] = int(rows[i+3])
	}
	for _, section := range doc.Sections {
		if strings.TrimSpace(strings.ToLower(section.Name)) != "guest gift" {
			continue
		}
		tokens := doc.Tokens[section.Start:section.End]
		if len(tokens) < 4 || (len(tokens)-1)%3 != 0 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int < limits[0] {
			return nil, ErrUnavailable
		}
		tier := GuestGiftTier{Maximum: tokens[0].Int, ByKind: map[byte][]repository.MailAttachment{}}
		for i := 1; i < len(tokens); i += 3 {
			for _, token := range tokens[i : i+3] {
				if token.Kind != pvf.TokenInt {
					return nil, ErrUnavailable
				}
			}
			kind, id, count := tokens[i].Int, tokens[i+1].Int, tokens[i+2].Int
			if kind < 0 || kind > 255 || rules.Capacity[byte(kind)] == 0 || id <= 0 || id > 0xffffffff || count <= 0 || count > 0xffffffff {
				return nil, ErrUnavailable
			}
			tier.ByKind[byte(kind)] = append(tier.ByKind[byte(kind)], repository.MailAttachment{ItemID: id, Count: count})
		}
		rules.Gifts = append(rules.Gifts, tier)
	}
	sort.Slice(rules.Gifts, func(i, j int) bool { return rules.Gifts[i].Maximum < rules.Gifts[j].Maximum })
	if len(rules.Gifts) == 0 || rules.Gifts[len(rules.Gifts)-1].Maximum < rules.MaximumGold {
		return nil, fmt.Errorf("%w: guest gift tiers incomplete", ErrUnavailable)
	}
	for i, tier := range rules.Gifts {
		if i > 0 && rules.Gifts[i-1].Maximum == tier.Maximum {
			return nil, ErrUnavailable
		}
		for kind := range rules.Capacity {
			if len(tier.ByKind[kind]) == 0 {
				return nil, ErrUnavailable
			}
		}
	}
	return rules, nil
}

func (r *GuestRules) GiftFor(gold int64, kind byte) ([]repository.MailAttachment, error) {
	if r == nil || gold < r.MinimumGold || gold > r.MaximumGold {
		return nil, ErrUnavailable
	}
	for _, tier := range r.Gifts {
		if gold <= tier.Maximum && len(tier.ByKind[kind]) > 0 {
			return append([]repository.MailAttachment(nil), tier.ByKind[kind]...), nil
		}
	}
	return nil, ErrUnavailable
}
