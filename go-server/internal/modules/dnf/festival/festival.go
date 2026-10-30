// Package festival implements the private, account-wide historical gift counters.
// Rules are PVF data. Paid checkout persists counters and an outbox atomically;
// mailbox delivery consumes that outbox in an account/mailbox transaction.
package festival

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const PVFPath = "etc/chn_cerashop_multibonus.etc"
const MetadataKey = "private_festival_v1"

var ErrInvalid = errors.New("invalid festival catalog/state")
var ErrReplay = errors.New("festival purchase already committed")

type Reward struct{ Item, Count uint32 }
type Period struct {
	ID         uint32
	Name       string
	RepeatFrom uint32
	Repeat     Reward
	Tiers      map[uint32]Reward
}
type Catalog struct {
	Periods     map[uint32]Period
	Commodities map[uint32]uint32
}
type Award struct {
	Period, At uint32
	Character  string
	Reward     Reward
}
type Receipt struct{ ID, Digest string }
type State struct {
	Version  int
	Counts   map[uint32]uint32
	Pending  []Award
	Receipts []Receipt
}

func rowSection(d *pvf.Document, name string, width int) ([][]pvf.Token, error) {
	ts, ok := d.Section(name)
	if !ok || len(ts) == 0 || len(ts)%width != 0 {
		return nil, ErrInvalid
	}
	count := 0
	for _, s := range d.Sections {
		if strings.EqualFold(strings.Trim(s.Name, " []\t\r\n"), name) {
			count++
		}
	}
	if count != 1 {
		return nil, ErrInvalid
	}
	out := make([][]pvf.Token, 0, len(ts)/width)
	for i := 0; i < len(ts); i += width {
		out = append(out, ts[i:i+width])
	}
	return out, nil
}
func num(t pvf.Token) (uint32, error) {
	if t.Kind != pvf.TokenInt || t.Int < 0 || t.Int > math.MaxUint32 {
		return 0, ErrInvalid
	}
	return uint32(t.Int), nil
}
func Parse(d *pvf.Document) (*Catalog, error) {
	if d == nil {
		return nil, ErrInvalid
	}
	_, a := d.Section("private festival periods")
	_, b := d.Section("private festival commodities")
	_, c := d.Section("private festival rewards")
	if !a && !b && !c {
		return nil, nil
	}
	cat := &Catalog{Periods: map[uint32]Period{}, Commodities: map[uint32]uint32{}}
	rows, e := rowSection(d, "private festival periods", 5)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		id, e1 := num(r[0])
		tail, e2 := num(r[2])
		item, e3 := num(r[3])
		count, e4 := num(r[4])
		name := strings.TrimSpace(r[1].Value)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id == 0 || name == "" || len(name) > 64 || ((tail == 0) != (item == 0)) || ((item == 0) != (count == 0)) || count > 9999 {
			return nil, ErrInvalid
		}
		if _, ok := cat.Periods[id]; ok {
			return nil, ErrInvalid
		}
		cat.Periods[id] = Period{id, name, tail, Reward{item, count}, map[uint32]Reward{}}
	}
	if len(cat.Periods) > 16 {
		return nil, ErrInvalid
	}
	rows, e = rowSection(d, "private festival commodities", 2)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		id, e1 := num(r[0])
		pid, e2 := num(r[1])
		_, ok := cat.Periods[pid]
		if e1 != nil || e2 != nil || id == 0 || !ok || cat.Commodities[id] != 0 {
			return nil, ErrInvalid
		}
		cat.Commodities[id] = pid
	}
	rows, e = rowSection(d, "private festival rewards", 4)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		v := [4]uint32{}
		for i, t := range r {
			v[i], e = num(t)
			if e != nil {
				return nil, e
			}
		}
		p, ok := cat.Periods[v[0]]
		if !ok || v[1] == 0 || v[1] > 10000 || v[2] == 0 || v[3] == 0 || v[3] > 9999 || p.Tiers[v[1]].Item != 0 || (p.RepeatFrom > 0 && v[1] >= p.RepeatFrom) {
			return nil, ErrInvalid
		}
		p.Tiers[v[1]] = Reward{v[2], v[3]}
	}
	return cat, nil
}
func Load(raw string) (State, error) {
	s := State{Version: 1, Counts: map[uint32]uint32{}}
	if raw != "" {
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &s) != nil || s.Version != 1 || s.Counts == nil || len(s.Pending) > 4096 || len(s.Receipts) > 128 {
			return State{}, ErrInvalid
		}
	}
	for _, a := range s.Pending {
		if a.Period == 0 || a.At == 0 || a.Character == "" || a.Reward.Item == 0 || a.Reward.Count == 0 {
			return State{}, ErrInvalid
		}
	}
	return s, nil
}
func Save(s State) (string, error) {
	b, e := json.Marshal(s)
	if e != nil || len(b) > 1<<20 {
		return "", ErrInvalid
	}
	return string(b), nil
}
func CheckReceipt(s State, id, digest string) error {
	if id == "" || digest == "" {
		return ErrInvalid
	}
	for _, r := range s.Receipts {
		if r.ID == id {
			if r.Digest != digest {
				return ErrInvalid
			}
			return ErrReplay
		}
	}
	return nil
}

// Apply runs on the transaction-owned account clone, never on a global cache.
// Quantities here are paid package units after resolving server catalog rows.
func Apply(c *Catalog, s *State, units map[uint32]uint32, character, id, digest string) error {
	if c == nil || s == nil || character == "" || len(units) == 0 {
		return ErrInvalid
	}
	if e := CheckReceipt(*s, id, digest); e != nil {
		return e
	}
	keys := make([]uint32, 0, len(units))
	for k := range units {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	total := uint32(0)
	for _, pid := range keys {
		n := units[pid]
		p, ok := c.Periods[pid]
		if !ok || n == 0 || n > 64 || total > 64-n || s.Counts[pid] > math.MaxUint32-n {
			return ErrInvalid
		}
		total += n
		old := s.Counts[pid]
		for off := uint32(1); off <= n; off++ {
			at := old + off
			r, ok := p.Tiers[at]
			if p.RepeatFrom > 0 && at >= p.RepeatFrom {
				r = p.Repeat
				ok = true
			}
			if ok {
				s.Pending = append(s.Pending, Award{pid, at, character, r})
			}
		}
		s.Counts[pid] = old + n
	}
	if len(s.Pending) > 4096 {
		return ErrInvalid
	}
	s.Receipts = append(s.Receipts, Receipt{id, digest})
	if len(s.Receipts) > 128 {
		s.Receipts = s.Receipts[len(s.Receipts)-128:]
	}
	return nil
}
func CompletionKey(a Award) string { return fmt.Sprintf("festival:v1:%d:%d", a.Period, a.At) }
