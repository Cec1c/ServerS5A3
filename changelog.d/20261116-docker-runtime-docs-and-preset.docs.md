---
category: 功能修复
---

# 重写公开文档首页并补齐 Docker 运行配置预设包

## 变更

- 重写仓库首页 `README.md` 与 `Docs/DOCKER_RUNTIME_REFERENCE.md`，将 Docker 运行配置包、常见“假 BUG”排障速查表（如青龙/黄龙/魂大会因缺 `dungeon_tournament: "vendor"` 导致进图无怪、2365 赛马闪退、双组 Buff/分解显示/城镇同屏与公会开关未启用）、当前公开 Releases 客户端/PVF 配对指南及一键复制给 Agent 的部署提示词置于首屏核心路径。
- 新增可直接合并入 `runtime/docker/instance.json` 的推荐配置预设包 `deploy/templates/docker-instance.recommended.json`，并同步更新 `deploy/templates/docker-server.redacted.env`、`Docs/BUILD_AND_DEPLOY.md`、`Docs/DOCKER_LOCAL.md` 与 `Docs/BUFF_GROUP_DEPLOYMENT.md`。
- 在公开 `deploy/docker/manage.py` 同步补齐已公开客户端与服务端源码对应的配置映射：识别 V215 正式客户端 SHA256（`28e04d1d…`），支持 `disjoint_epic_display`（`off`/`paired-v1`）、`town_display_only`（`off`/`on`）、`gold_diagnostic_character` 与 `DNFBRIDGE_EXPERIMENTAL_PEAK`（`off`/`on`）。

## 验证

- 运行 `python -m unittest discover -s deploy/docker -p "test_*.py" -v`，65 项用例通过（9 项需独立 Linux 根文件系统检查按设计跳过），验证推荐预设包合并生成 `server.env` 全部目标键生效且未配置默认输出保持不变。
- 运行 `python scripts/ci/changelog_fragments.py check --base be313414b24c1f005bbb50f3813c7b7bbb14475e`、`git diff --check`、本地 Markdown 相对路径与标题锚点校验通过，GitHub `/markdown` GFM 渲染核对首屏徽章与 Alerts 样式通过。本次为公开文档与本地 Docker 生成器同步，不触发线上游戏服务切换或重启。
