---
category: 功能修复
---

# 修复账号统计窗口的排行榜按钮无反应

## 变更

- 排行榜用客户端窗口消息 `200/201` 分派打开和查询结果。战令红点通知处理器排在排行榜之前，与它共用了这两个编号。将战令结果和重置改为 `300/301`，发送和接收一并调整。
- `scripts/client/patch_account_rank_message_collision.py` 校验固定输入和输出的完整 SHA256，只替换四个立即数的八个字节。战令和排行榜仍使用原有游戏网络协议。
- 使用 `python scripts/client/patch_account_rank_message_collision.py --input <DNF.exe>` 检查固定输入，需要生成独立候选时追加 `--output <候选EXE>`。其他客户端版本需在 `BattleEntry` 源码中隔离 `ResultMessage` 和 `ResetMessage`，并核对该版本的消息分派。

## 验证

- 使用固定旧版及修正版的消息分派函数做隔离验证。旧版复现 `200` 被提前消费、`201` 误触发重置。修正版的 26 次点击均转入排行榜打开逻辑，`300/301` 归战令处理。
- Windows 映像检查通过。游戏内窗口显示、榜单数据和战令提醒待实机确认。
