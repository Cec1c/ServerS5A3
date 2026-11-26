package weaponskin

import (
	"errors"
	"math"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skinwarehouse"
)

const (
	BeiyunWeaponID     = 101010327
	BeiyunWeaponPath   = "equipment/character/swordman/weapon/hsword/101010327.equ"
	MagicMoldID        = 490704411
	MagicMoldPath      = "stackable/cash/chn_490704411.stk"
	BeiyunProductID    = 10308800
	BeiyunProductPath  = "stackable/10308001/10308800.stk"
	SecretMoldID       = 490704427
	SecretMoldPath     = "stackable/cash/chn_490704427.stk"
	AccountProductID   = 490012391
	AccountProductPath = "stackable/490012001/chn_490012391.stk"
	ProductSkinKey     = "weapon_skin_id"
)

type BeiyunRecipe uint8

const (
	MagicRecipe BeiyunRecipe = iota
	SecretRecipe
)

type BeiyunRecipeItems struct {
	MoldID, ProductID     uint32
	MoldPath, ProductPath string
}

func (r BeiyunRecipe) Items() BeiyunRecipeItems {
	if r == SecretRecipe {
		return BeiyunRecipeItems{SecretMoldID, AccountProductID, SecretMoldPath, AccountProductPath}
	}
	return BeiyunRecipeItems{MagicMoldID, BeiyunProductID, MagicMoldPath, BeiyunProductPath}
}

var ErrAlreadyRegistered = errors.New("当前角色已经登记该武器外观")

// RegisterBeiyun 只登记角色外观。调用方须在同一角色资产事务扣除成品。
func RegisterBeiyun(character dnfrepo.CharacterRecord, now time.Time) (dnfrepo.CharacterRecord, error) {
	a, _ := FromAppearance(SampleID)
	return Register(character, a, now)
}

func Register(character dnfrepo.CharacterRecord, a Appearance, now time.Time) (dnfrepo.CharacterRecord, error) {
	if !a.Allows(character.Job) {
		return dnfrepo.CharacterRecord{}, ErrCharacter
	}
	if Registered(character, a) {
		return dnfrepo.CharacterRecord{}, ErrAlreadyRegistered
	}
	revision := character.Stats[revisionStat]
	if revision < 0 || revision >= math.MaxUint32 {
		return dnfrepo.CharacterRecord{}, ErrUnavailable
	}
	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = make(map[string]int64, 2)
	}
	character.Stats[a.registrationKey()] = int64(a.ID)
	skinwarehouse.RecordAcquisition(character.Stats, skinwarehouse.Weapon, a.ID, now)
	character.Stats[revisionStat] = revision + 1
	character.UpdatedAt = now.UTC()
	return character, nil
}
