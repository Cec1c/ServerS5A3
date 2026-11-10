---
category: 功能修复
---

# 本地补丁栈记录上游客户端 EXE 更新

## 变更

- 新增 `scripts/client/local-patches/UPSTREAM.md`：记录上游客户端发布页（GitGud，GitHub 镜像供脚本读取），以及 V207～V211 每一版的基线、内容、服务端配套开关、EXE 摘要和我方派生版本。
- 新增 `check_upstream.py`：从 GitHub 镜像读取 Release 说明里的 `client-release-sync:v1` 同步信息，标出比已记录的最新版本更晚发布的版本；`--download` 下载后核对 zip 和 EXE 摘要，再解出 DNF.exe。

## 验证

- 列表与 GitHub 上 V207～V211 的摘要一致，无新增未记录版本；下载 V210 后 zip 与 EXE 摘要核对通过，`apply_stack.py --check` 显示两个本地补丁均可自动应用。
