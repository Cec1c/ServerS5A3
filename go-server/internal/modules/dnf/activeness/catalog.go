// Package activeness owns the private PVF daily activity state and claims.
package activeness

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"longheng.io/server/internal/modules/dnf/pvf"
	"math"
	"strings"
)

const PVFPath = "etc/chn_actionpointsystem.etc"

var (
	ErrCatalog        = errors.New("invalid private activity PVF")
	ErrState          = errors.New("invalid activity state")
	ErrOwner          = errors.New("activity owner unavailable")
	ErrNotReady       = errors.New("activity points insufficient")
	ErrStaleDay       = errors.New("activity day changed")
	ErrCatalogChanged = errors.New("activity configuration changed during service day")
)

type Task struct{ ID, Target, Points uint16 }
type Reward struct {
	ID, Required                                                      uint16
	HappyTokens, PotionID, PotionCount, InvitationID, InvitationCount uint32
}
type Catalog struct {
	Tasks       [8]Task
	Rewards     [3]Reward
	Fingerprint int64
}

func section(doc *pvf.Document, name string, count int) ([]int64, error) {
	n := 0
	for _, s := range doc.Sections {
		if strings.EqualFold(strings.Trim(s.Name, " []\t\r\n"), name) {
			n++
		}
	}
	ts, ok := doc.Section(name)
	if n != 1 || !ok || len(ts) != count {
		return nil, ErrCatalog
	}
	v := make([]int64, count)
	for i, t := range ts {
		if t.Kind != pvf.TokenInt || t.Int < 0 || t.Int > math.MaxUint32 {
			return nil, ErrCatalog
		}
		v[i] = t.Int
	}
	return v, nil
}

// Missing private sections disable this extension on other PVF baselines.
// A declared but malformed configuration is an error, never a default reward.
func ParseCatalog(doc *pvf.Document) (*Catalog, error) {
	if doc == nil {
		return nil, ErrCatalog
	}
	_, hasTasks := doc.Section("private activity tasks")
	_, hasRewards := doc.Section("private activity rewards")
	if !hasTasks && !hasRewards {
		return nil, nil
	}
	tv, err := section(doc, "private activity tasks", 24)
	if err != nil {
		return nil, err
	}
	rv, err := section(doc, "private activity rewards", 21)
	if err != nil {
		return nil, err
	}
	c := &Catalog{}
	total := int64(0)
	for i := range c.Tasks {
		v := tv[i*3:]
		if v[0] != int64(i+1) || v[1] < 1 || v[1] > 65535 || v[2] < 1 || v[2] > 65535 {
			return nil, ErrCatalog
		}
		c.Tasks[i] = Task{uint16(v[0]), uint16(v[1]), uint16(v[2])}
		total += v[2]
	}
	if total > 65535 {
		return nil, ErrCatalog
	}
	for i := range c.Rewards {
		v := rv[i*7:]
		if v[0] != int64(i+1) || v[1] < 1 || v[1] > total || v[2] < 1 || v[3] < 1 || v[4] < 1 || v[4] > 9999 || v[6] > 999999 || (v[5] == 0) != (v[6] == 0) || (i > 0 && v[1] <= int64(c.Rewards[i-1].Required)) {
			return nil, ErrCatalog
		}
		c.Rewards[i] = Reward{uint16(v[0]), uint16(v[1]), uint32(v[2]), uint32(v[3]), uint32(v[4]), uint32(v[5]), uint32(v[6])}
	}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	c.Fingerprint = int64(binary.LittleEndian.Uint64(h[:8]) & math.MaxInt64)
	if c.Fingerprint == 0 {
		return nil, ErrCatalog
	}
	return c, nil
}
