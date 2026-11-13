---
category: 功能修复
---

# 允许生成 V212 双组 Buff 配置

## 变更

- `deploy/docker/manage.py` 的 `runtime_compatibility` 白名单新增精确的 `DNFBRIDGE_BUFF_GROUP_SYNC=on` 和 `off`。未配置时仍不注入该变量，不改变服务端默认关闭行为或 Go 协议实现。
- 新增 V212 配置片段和部署说明，限定 `official-24.9` 与 EXE SHA256 `69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544` 配对使用。
- 历史迁移回归按现有按技能隔离规则检查：选择新技能时装备为空，旧技能绑定仍保存，切回旧技能可恢复；保持生产持久化逻辑。

## 验证

- Python 专项验证覆盖 `on`/`off` 完整生成、未配置及空兼容对象不注入、其他环境字节保持，以及布尔值、大小写变体、`true`、`1` 等非精确值拒绝。
- 未运行 Docker、游戏或线上环境；未捕获搭建者现场，也未据此声明部署或玩家实机验收。
