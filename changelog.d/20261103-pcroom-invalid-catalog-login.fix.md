---
category: 功能修复
---

# 修复在线奖励表配置错误时所有角色进城断线

## 变更

- `etc/pcroomtimepoint.etc` 的每日奖励不是原生面板支持的四档时，加载器按原规则拒绝，但城镇初始化把拒绝当作致命错误返回，导致每一次选角进城都断线。现在改为记录 `game-spend-time-initialization-deferred` 降级事件后继续登录，在线活动保持不可用。
- 选角页 Lv 挑战和等级瞄准的活动目录在同一错误下改为记录 `game-ported-selector-catalog-deferred` 后继续，不再断开选角流程。
- 目录首次被拒绝时在服务端日志记一条带原因的 WARN，不必再从脱敏摘要反查。
- 仍不回退旧 2347 方案、不伪造奖励；错误继续缓存，PVF 修正后需重新加载服务端。无协议、客户端、资源或玩家数据变化。

## 验证

- 现场：2026-11-03 90CN 上线的 PVF 把每日奖励改成五档，期间九次进城断线的脱敏摘要与“initialize selected town actor before transition: load current spend-time catalog: pcroom [daily reward items] has invalid shape”逐字一致，本地用同一份 PVF 复现。
- 新增领域回归固定五档每日奖励被拒绝；桥接回归覆盖城镇初始化与选角目录在目录错误下继续、留下降级事件且不掩盖缓存错误，修复前失败、修复后通过。
- 本地 `go vet`、`internal/modules/dnf/...` 测试通过；`dnfbridge` 全量在修复前后失败集合一致，均为缺少运行时 PVF 的既有用例。实机进城仍须在部署后验收。
