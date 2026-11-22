# 备份保留与历史快照回收

本工具入口为 `deploy/remote/backup_retention.py`。默认只生成计划，只有显式 `--apply` 才删除。

## 保留策略

- 当前 `active-game-baseline.json` 的恢复点始终保护。
- 按备份引用身份保留最近 `--keep-deployments` 个健康部署，默认 3。
- 数据库 dump 保留最近 3 份、最近 7 天每日一份、最近 4 周每周一份。
- 旧目录只回收指纹完全相同的重复项。
- 受管理的历史快照默认按批次回收，当前批次上限为 2 个目录。

## 引用保护

工具从以下来源建立引用关系：

- 当前活动基线。
- `game-deployment.json` 健康完成回执。
- 未完成或未知状态的部署回执。
- 没有 `game-deployment.json` 的 `game-updates/*/backup-receipt.json`。
- `game-updates` 下尚未完成的准备目录。
- 容器 bind mount 指向的路径。

所有字符串路径递归解析。指向 `runtime/backups` 或 `runtime/game-updates` 的引用会保护对应一级目录，并从被保护目录继续读取 `backup-receipt.json`、`game-deployment.json`、`prepared-game.json`、`game-rollback.json`。符号链接或越界路径拒绝。

挂载清单默认必须来自完整的 Docker 快照。缺少 Docker CLI 或可用 socket 时，工具拒绝生成可执行计划，不会把未知挂载当成空清单。没有 Docker 的测试或离线夹具必须显式传入 `mount_runner`，例如 `mount_runner=lambda: []` 表示调用方确认不存在容器挂载。`apply` 会重新获取同一来源的挂载快照，来源不可用时整批拒绝删除。

旧残缺回执引用的 dump 已不存在时，只有同时满足以下条件才不回滚清理：

- 回执含 `beforeImage`。
- 同目录 `prepared-game.json` 绑定相同 `beforeImage`。
- 存在独立健康切换，目标镜像不同于 `beforeImage`，且 `baseCommit` 匹配。

满足条件时只在计划中记录 `missingSupersededPending`，不视为错误。

## 批量回收历史快照

```sh
python3 deploy/remote/backup_retention.py --runtime-dir runtime \
  --include-game-updates --max-game-update-remove 2
```

只有同时满足以下条件的目录进入回收计划：

- 普通目录，非符号链接。
- 有健康、完整的 `game-deployment.json`。
- 未被当前活动、历史回执、未完成准备或容器挂载引用。
- 指纹在计划和执行时一致。

未完成目录、非健康目录、受保护目录和本轮批次之外的目录保持不动。执行时若出现新挂载或内容变化，整批拒绝。

## 应用

```sh
python3 deploy/remote/backup_retention.py --runtime-dir runtime --apply \
  --keep-deployments 3 --include-game-updates --max-game-update-remove 2
```

计划回执写入 `runtime/backup-retention/<timestamp>.json`。定时入口由 `install_backup_retention.py` 写入，默认：

```sh
python3 deploy/remote/install_backup_retention.py --root /srv/dnf90 \
  --keep-deployments 3 --max-game-update-remove 2
```

公开入口 `run` 在 `runtime/guild-deployment.lock` 下完成计划与执行。直接调用 `apply` 的集成方必须自行持有同一把锁。

公开部署器的 `cleanup` 调用位于 `deploy/windows/game_update.py`，当前只传入 `--apply`。若手动 `cleanup` 也要回收历史快照，调用方需在该调用点追加 `--include-game-updates --max-game-update-remove 2`；未改该文件时，历史快照回收由上面的定时入口执行。

## 回归

```sh
cd deploy/remote
python3 -m unittest -v test_backup_retention.py test_backup_retention_references.py
```

新增覆盖：残缺回执保护、缺失 dump 的独立替代证明、传递引用、容器挂载、历史快照分批回收、计划后内容变化拒绝、未知挂载清单拒绝删除。
