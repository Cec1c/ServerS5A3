---
category: 功能修复
---

# 减少副本移动与战斗的地图复制

## 变更

- 增加 RuntimeScene，避免热点复制原始地图 token；保留动态状态隔离，移动诊断改为标量读取。需要原始语法的 Scene 接口不变。

## 验证

- Docker Linux worldmap、领域及 bridge 全量通过；worldmap race、快照等价/隔离及分配基准通过。
- 本批组合验证；各提交有依赖，按整批累计。切换与游戏验收另行记录。
