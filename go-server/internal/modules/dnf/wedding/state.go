package wedding

import (
	"errors"
	"fmt"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	Single byte = iota
	Engaged
	Reserved
	InCeremony
	Married
	statPrefix = "wedding_v1_"
)

var (
	ErrUnavailable = errors.New("wedding transaction unavailable")
	ErrOwnership   = errors.New("wedding character ownership mismatch")
	ErrState       = errors.New("wedding relationship state invalid")
	ErrEligibility = errors.New("wedding participants ineligible")
	ErrQuest       = errors.New("wedding preparation quests incomplete")
	ErrGold        = errors.New("wedding gold insufficient")
	ErrItem        = errors.New("wedding item unavailable")
	ErrReplay      = errors.New("wedding operation already applied")
)

type Actor struct{ AccountID, CharacterID string }

// State is a projection of namespaced numeric fields in the owned character
// aggregate. Pair mutations use the existing two-character asset transaction,
// so both links, ring levels, costs and room state share one commit.
type State struct {
	RelationToken                        int64
	PartnerID                            int64
	Phase                                byte
	Ring                                 int
	Kind                                 byte
	CeremonyAt, MarriedAt, CooldownUntil int64
	LastWeddingDay, LastLoveDay          int64
	RoomPoints, LovePoints               int64
	CeremonyChannel, CeremonyArea        int64
	CeremonyReadyAt                      int64
}

func NumericID(id string) (int64, error) {
	n, e := strconv.ParseInt(id, 10, 32)
	if e != nil || n <= 0 || n > 65535 || strconv.FormatInt(n, 10) != id {
		return 0, ErrOwnership
	}
	return n, nil
}

func Read(c r.CharacterRecord) (State, error) {
	s := c.Stats
	v := State{PartnerID: s[statPrefix+"partner"], Phase: byte(s[statPrefix+"phase"]), Ring: int(s[statPrefix+"ring"]), Kind: byte(s[statPrefix+"kind"]), CeremonyAt: s[statPrefix+"ceremony_at"], MarriedAt: s[statPrefix+"married_at"], CooldownUntil: s[statPrefix+"cooldown_until"], LastWeddingDay: s[statPrefix+"wedding_day"], LastLoveDay: s[statPrefix+"love_day"], RoomPoints: s[statPrefix+"room_points"]}
	v.LovePoints = s[statPrefix+"love_points"]
	v.CeremonyChannel, v.CeremonyArea = s[statPrefix+"ceremony_channel"], s[statPrefix+"ceremony_area"]
	v.CeremonyReadyAt = s[statPrefix+"ceremony_ready_at"]
	v.RelationToken = s[statPrefix+"relation_token"]
	if v.RelationToken < 0 || v.RelationToken > 0xffffffff {
		return State{}, ErrState
	}
	if s[statPrefix+"phase"] < 0 || s[statPrefix+"phase"] > int64(Married) || s[statPrefix+"kind"] < 0 || s[statPrefix+"kind"] > 255 || v.Ring < 0 || v.RoomPoints < 0 || v.PartnerID < 0 || v.PartnerID > 65535 || v.CeremonyAt < 0 || v.MarriedAt < 0 || v.CooldownUntil < 0 {
		return State{}, ErrState
	}
	if v.LovePoints < 0 || v.LovePoints > 0xffffffff || v.RoomPoints > 0xffffffff || v.MarriedAt > 0xffffffff || v.CeremonyAt > 0xffffffff {
		return State{}, ErrState
	}
	if v.Phase == Single && v.PartnerID != 0 || v.Phase != Single && v.PartnerID == 0 {
		return State{}, ErrState
	}
	if v.CeremonyChannel < 0 || v.CeremonyChannel > 255 || v.CeremonyArea < 0 || v.CeremonyArea > 255 {
		return State{}, ErrState
	}
	if v.CeremonyReadyAt < 0 || v.CeremonyReadyAt > 0xffffffff || v.CeremonyReadyAt != 0 && (v.CeremonyReadyAt < v.CeremonyAt || v.CeremonyReadyAt > v.CeremonyAt+CeremonyAdmissionGraceSeconds) {
		return State{}, ErrState
	}
	if (v.Phase == Reserved || v.Phase == InCeremony) && !((CeremonyRoute{Channel: byte(v.CeremonyChannel), Area: byte(v.CeremonyArea)}).Valid()) {
		return State{}, ErrState
	}
	if id, err := NumericID(c.CharacterID); err != nil || id == v.PartnerID {
		return State{}, ErrState
	}
	return v, nil
}

func write(c *r.CharacterRecord, s State) {
	if c.Stats == nil {
		c.Stats = make(map[string]int64)
	}
	for k, v := range map[string]int64{"partner": s.PartnerID, "phase": int64(s.Phase), "ring": int64(s.Ring), "kind": int64(s.Kind), "ceremony_at": s.CeremonyAt, "married_at": s.MarriedAt, "cooldown_until": s.CooldownUntil, "wedding_day": s.LastWeddingDay, "love_day": s.LastLoveDay, "room_points": s.RoomPoints} {
		c.Stats[statPrefix+k] = v
	}
	c.Stats[statPrefix+"love_points"] = s.LovePoints
	c.Stats[statPrefix+"ceremony_channel"], c.Stats[statPrefix+"ceremony_area"] = s.CeremonyChannel, s.CeremonyArea
	c.Stats[statPrefix+"ceremony_ready_at"] = s.CeremonyReadyAt
	c.Stats[statPrefix+"relation_token"] = s.RelationToken
}

func ValidatePair(a, b r.CharacterRecord) (State, State, error) {
	x, err := Read(a)
	if err != nil {
		return State{}, State{}, err
	}
	y, err := Read(b)
	if err != nil {
		return State{}, State{}, err
	}
	ai, e1 := NumericID(a.CharacterID)
	if x.RelationToken != y.RelationToken {
		return State{}, State{}, ErrState
	}
	bi, e2 := NumericID(b.CharacterID)
	if e1 != nil || e2 != nil || a.AccountID == "" || b.AccountID == "" || a.AccountID == b.AccountID || x.PartnerID != bi || y.PartnerID != ai || x.Phase != y.Phase || x.Ring != y.Ring || x.Kind != y.Kind || x.CeremonyAt != y.CeremonyAt || x.MarriedAt != y.MarriedAt || x.RoomPoints != y.RoomPoints || x.CeremonyChannel != y.CeremonyChannel || x.CeremonyArea != y.CeremonyArea || x.CeremonyReadyAt != y.CeremonyReadyAt {
		return State{}, State{}, ErrState
	}
	return x, y, nil
}

func eligible(a, b r.CharacterRecord, rules *Rules) error {
	if rules == nil || int64(a.Level) < rules.MinimumLevel || int64(b.Level) < rules.MinimumLevel || a.AccountID == "" || a.AccountID == b.AccountID {
		return ErrEligibility
	}
	female := func(job string) (bool, bool) {
		switch job {
		case "0", "2", "4", "7", "8", "9", "13", "15":
			return false, true
		case "1", "3", "5", "6", "10", "11", "12", "14":
			return true, true
		}
		return false, false
	}
	x, ok1 := female(a.Job)
	y, ok2 := female(b.Job)
	if !ok1 || !ok2 || x == y {
		return ErrEligibility
	}
	return nil
}

func (s State) String() string {
	return fmt.Sprintf("phase=%d partner=%d ring=%d", s.Phase, s.PartnerID, s.Ring)
}
