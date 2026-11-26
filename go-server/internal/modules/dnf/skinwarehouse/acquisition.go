// Package skinwarehouse stores acquisition times shared by the cosmetic tabs.
package skinwarehouse

import (
	"strconv"
	"strings"
	"time"
)

type Kind uint8

const (
	Weapon Kind = iota + 1
	DamageFont
)

const (
	weaponPrefix = "skin_got_w_"
	fontPrefix   = "skin_got_f_"
)

type Acquisition struct {
	Kind   Kind
	ID     uint32
	Millis int64
}

// RecordAcquisition is called inside the transaction that acquires ownership.
func RecordAcquisition(stats map[string]int64, kind Kind, id uint32, now time.Time) {
	if stats == nil || id == 0 || now.UnixMilli() <= 0 {
		return
	}
	switch kind {
	case Weapon:
		stats[weaponPrefix+strconv.FormatUint(uint64(id), 10)] = now.UnixMilli()
	case DamageFont:
		stats[fontPrefix+strconv.FormatUint(uint64(id), 10)] = now.UnixMilli()
	}
}

func Acquisitions(stats map[string]int64) []Acquisition {
	var entries []Acquisition
	for key, millis := range stats {
		if millis <= 0 {
			continue
		}
		digits, kind := strings.TrimPrefix(key, weaponPrefix), Weapon
		if digits == key {
			digits, kind = strings.TrimPrefix(key, fontPrefix), DamageFont
			if digits == key {
				continue
			}
		}
		id, err := strconv.ParseUint(digits, 10, 32)
		if err == nil && id != 0 && digits == strconv.FormatUint(id, 10) {
			entries = append(entries, Acquisition{Kind: kind, ID: uint32(id), Millis: millis})
		}
	}
	return entries
}
