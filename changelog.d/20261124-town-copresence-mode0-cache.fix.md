---
category: 功能修复
---

# 城镇同屏 mode0 短 TTL 内存去重缓存与非同队轻量化投影

## 变更

- 在 `online_player_manager.go` 为 `official-24.9` 城镇轻量化展示（`displayOnly` `mode0` 及附带婚姻同屏提示）增加 `5s` 短 TTL、有界容量（`2048`）内存缓存及同键并发 singleflight 合并，确保 50 名观察者并发同屏进入城镇时仅构建一次 `mode0` 快照；缓存切片严格只读，写入 HP 前按需拷贝。
- 在换装/时装/宠物/称号簿/表情/婚姻/公会成员变更/组队变动/选角切换/离城与断线路径补充主动失效，并在构建完成后复核外观代次与角色绑定，防止构建中途变更造成脏缓存。
- 将 `town_display_only.go` 判定条件由“双方均为单人”收口为“双方不属于同一支多人队伍”，避免任意一方处于其他队伍时整屏路人退回重型 `mode1`，同时保留同队加入前自动升级完整 `mode1` 的既有契约。

## 验证

- 在 Docker Linux（`golang:1.26.5-bookworm`）执行 `go mod verify`、`go vet`、`go test -count=1 -race`（覆盖新增 `TestPR2TownMode0Cache*` / `TestPR2TownDisplayOnlyDifferentParties*` 与既有 `TestTownDisplay*` 全部回归，0 DATA RACE）以及 Linux `DNF90Server` 正式构建。
