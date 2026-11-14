# 本地验证与精简发布载荷

日常修复按影响验证，使用 Go 缓存，部署时重新构建 Linux 程序。本页仅覆盖开发机；不改远端 prepare/activate、数据库备份、线上控制器、云端工作流或公开/私有权威关系。

## Docker 本地入口

在干净已提交工作树使用 `LOCAL_VERIFY.bat`；跨平台也可运行 `python scripts/ci/local_verify.py`。每次给独立 `--output` 目录，已有目录拒绝覆盖。

```text
LOCAL_VERIFY.bat --package ./internal/modules/dnf/inventory/... --output runtime/verify/inventory-01
LOCAL_VERIFY.bat --package ./internal/services/dnfbridge --run "TestAuction" --output runtime/verify/auction-01
LOCAL_VERIFY.bat --package ./internal/modules/dnf/repository/mysql --race --uncached --output runtime/verify/mysql-01
LOCAL_VERIFY.bat --build-only --output runtime/verify/release-01
```

可重复 `--package`；`--run` 应命中原失败场景及相关边界，匹配零测试会失败。`--race` 只用于所选包；真实 MariaDB/PVF 等集成仍按其隔离夹具运行，这个入口不自动提供数据库或透传生产凭据。`--full` 选择原完整核心回归。默认 Go 1.26.5 镜像，可 `--image` 指定固定版本或 digest。

入口只读 bind 源码与 Git 元数据，共享 `dnf-local-gomod` / `dnf-local-gocache` 命名卷，输出目录可写。不克隆/复制全仓，不调用 WSL/Kali。缓存前缀可 `--cache-prefix` 指定；不执行全局 prune。Docker 不可用时修复 Docker。

所选测试后执行相关 vet、`go mod verify` 与 Linux/amd64 CGO0、timetzdata 的新 server build。输出日志、`DNF90Server` 和 `validation.json`，含精确提交/tree、命令、退出码、耗时及程序摘要。回执分开记录测试命令执行、实际新执行包和缓存命中包；全部命中缓存或 `--build-only` 时 testsExecuted=false，不把缓存结果/构建说成新测试。失败也保留回执，不能用于打包。

Go 缓存按输入决定复用。外部数据库内容、网络、未建模的时间因素不由普通缓存证明，相关调查用 `--uncached` 或原集成命令 `-count=1`。`-run` 只减少执行用例，仍编译整个包。纯工具改动运行工具专项，不需要先执行本入口的游戏构建。

## 主干前进与 quick

现有 `source_merge.py --quick` 已要求先累计最新主干，只补受影响检查；没有要求主干任意变化就全量重测。实际 merge/rebase 后审查 `git diff <已验证SHA> HEAD`，按运行输入和依赖确定新增验证。可复用没有变化的测试结论，但新声明必须绑定新 SHA，列出旧证据与本次差异判断。合并工具不自动执行候选代码、rebase 或沿用旧 SHA 声明。

Git 祖先校验保留累计历史，不能证明后续提交没有 revert；文件集合不相交也不能证明语义无依赖。事务、接口和共享依赖变化需覆盖调用方。纯文档累计不重跑游戏测试；未知影响按需要扩大，使用 Go 缓存避免无关子包重跑。

## 精简载荷生成（本地已实现，远端待协议接入）

```text
python deploy/windows/compact_game_package.py --source <干净累计源码目录> --build-receipt <本次validation.json> --base-commit <当前线上完整提交> --resource-identity <三项资源摘要.json> --output <新输出目录>
```

资源 JSON 只包含 `exeSha256`、`pvfSha256`、`channelSha256`，来自本次已核实线上基线。工具接受本地 Docker 入口的精确源码成功构建回执，并核验 ELF amd64 程序摘要、干净工作树和 base 祖先关系。实际权威 pull、在线基线/资源读回及维护授权由部署流程负责，本工具不创建授权。

只生成：

- `DNF90Server.gz`：正式程序。
- `deploy-tools.tar.gz`：已跟踪的 `deploy/docker/` Python/shell 运行工具，排除 `test_*`。附加部署依赖使用重复 `--tool deploy/.../name.py`，明确列入 manifest。
- `manifest.json`：`schema=dnf-game-compact-v1`，来源提交/tree、base、程序与三项资源身份、工具逐文件摘要、两项传输摘要/字节数、构建命令与回执摘要。没有全仓源码或 Git bundle。

输出目录必须位于源码外或 Git ignored 目录；只有成功写出 `manifest.json` 才形成完整包。压缩时同步计算程序摘要，确认压入的实际字节与构建回执一致，避免为同一程序反复全量读取。该包没有调用 SSH，不触发任何线上变化。

## 给远端开发者的接口

远端接收器须明确支持 `dnf-game-compact-v1` 后，才接入默认发布入口。它不是旧 `game-source-manifest.json` 的 `files` 完整仓库 schema，不能交给旧全仓解包/扫描逻辑。不要在旧协议失败时自动降级上传全仓。

接收器需对两个压缩件/解压程序和工具摘要验真，拒绝多余/重复/越界/链接成员；使用自己的已审查运行镜像模板。工具包不含需要源码编译的 Dockerfile。需要的私有执行脚本由调用方显式 `--tool` 加入，不从旧完整源码快照隐式取依赖。接收方保存来源、基线与配置回退引用；普通更新的数据库备份/镜像导出策略由远端负责人实施。

本地源码不改 `deploy/remote/*`、`deploy/control/*` 或云端 workflows。旧 `deploy/windows/game_update.py` 暂保留原协议，待线上接收契约就绪再接线，避免一边要求全仓、一边已停传的半套更新。现有固定发布机配置与队列归属保持；不恢复已退役 worker 身份。
