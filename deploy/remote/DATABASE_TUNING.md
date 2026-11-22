# 可选数据库 redo 与 flush_neighbors 调优

本工具路径为 `deploy/remote/database_tuning.py`。它只管理两个可选设置：

- `innodb_log_file_size`
- `innodb_flush_neighbors`

其余数据库参数只读校验。镜像、数据卷、引擎版本、buffer pool 与
`innodb_flush_log_at_trx_commit=1` 必须保持不变。应用和回退都要求游戏进程已停止。

## 默认只读

```sh
python3 deploy/remote/database_tuning.py --project servers5a3-remote \
  --redo-mib 256 --flush-neighbors 0
```

不传 `--redo-mib` 或 `--flush-neighbors` 时，工具只读取当前运行值和启动配置，返回承载计划，不写文件、不执行 `SET GLOBAL`。

## 应用与回退

```sh
python3 deploy/remote/database_tuning.py --project servers5a3-remote \
  --redo-mib 256 --flush-neighbors 0 --apply \
  --stage runtime/database-tuning/<request-id>

python3 deploy/remote/database_tuning.py --project servers5a3-remote \
  --rollback --stage runtime/database-tuning/<request-id>
```

阶段目录禁止预先存在。应用前写入 `baseline.json`、`plan.json` 和逐步写入标记；读回 live 变量、启动默认值和 owned option 文件摘要。失败只写不含命令输出的 `failure.json`，回退由显式 `--rollback` 执行。

`apply` 只在游戏停止后开始。若写入后失败，原数据库进程和数据卷保持不变，回退使用相同阶段目录。

默认锁文件为系统临时目录下的 `dnf90-database-tuning-<project>.lock`。生产接入建议用 `--lock-file` 指定一个跨进程、跨阶段目录共享的持久路径。`apply` 和 `rollback` 在整个检查和写入期间持有非阻塞排他锁，锁被占用时立即拒绝。已完成回退再次调用时先复核当前状态仍等于基线，再返回既有回执，不重放；`rollback-started` 已存在但未完成时拒绝继续，等待人工检查原动作。

## 安全边界

- 合格 redo 大小：96 MiB、256 MiB、512 MiB。
- 1 GiB redo 默认拒绝。只有显式 `--allow-unverified-1g-redo` 才进入磁盘空间检查，计划标记为 `disk-space-check-only`。该项仍缺生产磁盘合格验证。
- 可用空间必须覆盖 `max(当前 redo, 目标 redo) + 512 MiB`。
- 若 redo 或 flush_neighbors 已由本工具 owned 文件之外的配置提供，计划拒绝改写。
- owned 文件固定为 `/etc/mysql/conf.d/zz-dnf-optional-tuning.cnf`，仅含上述两个键。出现未知键、重复键、符号链接或非普通文件时拒绝。
- 回退只接受 baseline 或本次目标值。出现第三种运行值或第三方文件内容时拒绝覆盖。

## 公开入口接入点

本次不改 `deploy/remote/game_update.py`。部署集成时可在以下位置调用：

1. `prepare`：完成 `validate_before()` 后、写 `prepared-game.json` 前，调用 `plan`，把 `baselineSha256`、`targetValues`、`targetOptionSha256`、`changes`、`qualification` 写入准备回执。
2. `deploy`：完成 `validate_before()` 后、重启游戏前，调用 `apply`，阶段目录放在本次 `runtime/game-updates/<run-id>` 下。
3. 切换到新版本后：调用 `verify`；失败时先调用 `rollback`，再按既有流程回滚游戏镜像。
4. 既有 `rollback`：先调用 `rollback`，确认数据库参数恢复后再恢复旧镜像。

独立使用时不依赖公开入口，`--project` 与公开部署器默认项目名一致即可。

启动选项写在当前数据库容器的配置目录。使用容器可写层时，文件随该容器重启保留，重建容器后需要对新实例重新准备、读回并生成新的回执。

## 回归

```sh
cd deploy/remote
python3 -m unittest -v test_database_tuning.py
```

覆盖：空可选字段只读承载、同值仅持久化启动文件、redo/flush 应用读回、1 GiB 未合格默认拒绝、外部配置冲突、失败诊断、精确回退、并发锁、回退不可重放、foreign runtime 改动拒绝。
