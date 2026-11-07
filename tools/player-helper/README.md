# 玩家内辅参考实现（供参考）

本目录将下游已开发的内辅源码整理为可单独构建的参考实现，与本仓库服务端接口放在同一 Draft PR 供上游评估。它包含 Windows 插件宿主、自动拾取、便捷操作、原生金库桥接、SDK 夹具和登录器会话发布示例。

服务端默认关闭，宿主默认不启用插件。当前原生适配绑定一个明确的客户端文件，登录器、HTTPS 网关和发行签名仍需按采用方环境接入。本目录不提供可直接替换玩家文件的发行包。

详细设计及功能边界见 [接入说明](../../Docs/PLAYER_HELPER_REFERENCE.md)。

## 当前参考版本：宿主 0.4.4、便捷插件 0.1.3

Alt+方向键被拒绝时，宿主只更新便捷功能状态，保留面板原来的显示/隐藏状态。怪未清、门未开放或没有对应房间时不会自动唤出内辅；达到条件后可以再次顺图。Ctrl+Z 仍显式控制面板，维修确认及其他错误提示沿用现有处理。

Windows 界面夹具新增 `roomErrorsKeepPanelState`，对四方向分别检查面板隐藏/已开及游戏前台保持。源码版本与下游 1.4.12 内辅对应；同包登录器的勋章设计图计数适配属于客户端启动链，本目录的内辅更新只包含上述宿主修复及插件版本标识。

## 源码结构

| 路径 | 职责 |
| --- | --- |
| `src/Contracts`、`src/Runtime` | 插件契约、签名包校验、能力授权、生命周期和隔离加载上下文 |
| `src/Host` | Windows 消息泵、前台游戏识别、Ctrl+Z 面板、Alt 快捷键、会话管道与租约恢复 |
| `src/LootPlugin` | 金币及装备/消耗品各七档品级筛选 |
| `src/UtilityPlugin` | 出售、分解、修理、金库、合法相邻房间切换 |
| `native-ui` | 固定客户端的原生金库窗口桥接及独立测试进程 |
| `src/Tests`、`src/TestPlugin`、`src/TestGame` | SDK 生命周期/功能夹具、故障插件、Windows 假游戏窗口 |
| `integration` | 编入既有登录器的会话发布示例；不包含登录器 UI 或网关 |
| `sign_helper.go` | 发行者 RSA-PSS 签名入口，需自行配置私钥 |

## 构建与验证

需要 Python 3.10+、.NET SDK 8+。在仓库根目录运行：

```text
python tools/player-helper/build.py --out /absolute/path/to/new-helper-build
```

可用 `--dotnet /path/to/dotnet` 指定 SDK；需要交叉构建原生 Windows x86 客户端组件时，另装 Go 和 Zig 0.13.0，再传 `--zig /path/to/zig`。服务端仍按仓库 Linux 验证规则执行。

脚本先复制源码快照到新的输出目录，记录逐文件 SHA256，编译 SDK、Windows 宿主和登录器示例，运行 SDK 夹具；可选编译原生桥接与假游戏进程。`build.json` 区分编译、夹具、Windows 运行和发行状态。输出目录包含生成物与日志，不要提交到仓库。

参考宿主内置的是**仅供示例的公钥**，未提供对应私钥。采用方须替换 `src/Host/Trust/public.pem`，用自己的发行私钥签名插件清单。运行 `go run tools/player-helper/sign_helper.go` 可查看参数契约；配置入口为 `launcher_hub.signing_key_file`。签名域和清单格式见 `src/Runtime/Packages.cs`；签名成功不代表游戏内验收通过。

## 本次验证范围

提取后的 Go 专项、SDK 夹具及编译结果记录在 PR；Windows 实际界面、原生 hook 和完整登录链路需在采用方匹配环境验收。下游历史实机结果不作为本参考分支已经实机验收的证明。

本目录沿用 GPL-3.0，许可证见 [LICENSE](LICENSE)。
