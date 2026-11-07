# 上游客户端 EXE 跟踪

## 去哪里看

- 发布页（以这里为准）：https://gitgud.io/16ww/servers5a3/-/releases
  前面有 Cloudflare 验证，只能用浏览器打开。
- GitHub 镜像：https://github.com/Cec1c/ServerS5A3/releases
  上游会把每个客户端 Release 同步过来，说明里带 `client-release-sync:v1` 同步信息（GitGud 原链接、EXE 摘要、源码提交）。脚本读这里。
- 检查有没有新版本：`python -B check_upstream.py`，未记录到下表的版本会标“新”。
- 下载并核对：`python -B check_upstream.py --download <tag> --out <新目录>`，然后用 `apply_stack.py --check` 看我方补丁能否套上。

**上游出了新版本，就在下表最上面加一行。** 写清基于哪一版、改了什么、服务端要不要配套开关，以及我方是否在它上面派生了版本。

## 版本记录

| 上游版本 | 发布（北京时间） | 基于 | 内容 | 服务端配套 | EXE SHA256 | 我方派生 |
| --- | --- | --- | --- | --- | --- | --- |
| V211 `client-v211-town-display-20261104` | 11-04 05:28 | V210 | 普通城镇非队友入场只建显示对象，减少重复完整加载；组队前补齐完整角色 | 城镇显示模式开关，服务端默认关闭，需与 V211 配对后再开 | `d1930d5bb07ee8fae348e0c23551b0f12bcfaea67ed7bf53ab0c9099e195c309` | v2.1.1.1 |
| V210 `client-v210-disjoint-display-tcp-20261103` | 11-03 23:25 | V208（含未单独发布的 V209） | 史诗分解结果显示；游戏 TCP 收发缓冲 1024 → 262144 字节（`0x38c83f9`）；V209：卢克逐图进度接收（op602）、强化/增幅券提示修复 | 分解显示 `DNFBRIDGE_DISJOINT_EPIC_DISPLAY=paired-v1`，默认关闭，旧客户端在用时不能开；TCP 缓冲只改客户端 | `24372bf2df57cdaecc696752299dbe8c61e222806a697e3d1c6f57fbcaffbdae` | - |
| V208 `client-v208-epic-piece-shop-20261103` | 11-03 02:07 | V207 | 接收位图 1 字节 01 → 07，开放 817/818（深渊史诗碎片商店）；未重算 PE 校验和 | 碎片商店服务端代码 | `5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c` | v2.0.8.1、v2.0.8.2 |
| V207 `client-v207-level-challenge-20261102` | 11-02 04:42 | V206 | Lv 挑战角色可进入删除确认 | 创建时尊重“普通角色”选择 | `eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03` | v2.0.8 |

我方派生版本的完整登记（输入、补丁、输出摘要、发布状态）见 `stack.json` 的 `releases`。
