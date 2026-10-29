package dnfbridge

import (
	"fmt"
	"strconv"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 14602A0总是先登记已有登录本体，再登记备用actor。1456E80/1457120
// 及14571D0使用这个容器的0/1位置，不能使用组队名单或角色槽位序号。
// 领域角色仍按注册顺序保存，两种顺序只在协议边界转换。
func currentTagTournamentNativeRoleIDs(selected uint16, characters [2]repo.CharacterRecord) ([2]byte, error) {
	root := strconv.Itoa(int(selected))
	if selected == 0 || characters[0].CharacterID == "" || characters[1].CharacterID == "" || characters[0].CharacterID == characters[1].CharacterID {
		return [2]byte{}, fmt.Errorf("帝国竞技场原生双角色身份无效")
	}
	if characters[0].CharacterID == root {
		return [2]byte{0, 1}, nil
	}
	if characters[1].CharacterID == root {
		return [2]byte{1, 0}, nil
	}
	return [2]byte{}, fmt.Errorf("帝国竞技场原生双角色缺少登录本体")
}
