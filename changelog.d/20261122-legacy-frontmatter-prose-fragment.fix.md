---
category: 功能修复
---

# 兼容已合入无标题与简写分类历史变更片段以恢复公开 CI 绿灯

## 变更

- 在 `scripts/ci/changelog_fragments.py` 的 `legacy_sections=True` 兼容分支中，支持已合入历史片段在合法 frontmatter 后直接跟正文（如 `changelog.d/20261119-guild-installer-lock-handoff.fix.md`）以及公开目录已合入简写 `category: 修复` 片段（如 `changelog.d/20261121-channel-exit-wire.fix.md`），在不篡改已合入不可变片段字节与 SHA256 的前提下恢复公开仓 `Verify / scope` 绿灯。
- 收紧无 `--base` 校验与 `render` 的历史豁免判定：`inherited_notes()` 改为记录父提交精确 blob 字节，仅当工作区或目标提交内容与父提交原始字节完全一致时才允许走 legacy 解析，防止就地重写历史片段绕过校验；无 `--base` 校验的 Windows CRLF 检出归一化改为先对 `HEAD` 精确核验，再由父提交比对决定是否允许历史格式；`category: 修复` 简写严格限定在公开目录 `changelog.d/`，私有 `.pos5a3/changelog.d/` 明确拒收；无标题正文提取改用 `lstrip('\n').rstrip()` 保留缩进代码块首行空白。
- 在 `scripts/ci/test_changelog_fragments.py` 补齐无标题 frontmatter 片段、公开简写分类、私有目录拒收简写、未文档化后缀拒收、无基线篡改拒收、二次提交 CRLF 检出与 Markdown 缩进保留等回归测试。

## 验证

- 运行 `python -B -m unittest discover -s scripts/ci -p test_*.py -v`（163/163 全部通过）。
- 运行 `python -B scripts/ci/changelog_fragments.py check --scope public --base f103940d` 与 `check --scope public` 均在全部 230 条片段上通过（未修改任何已合入历史片段）；未部署线上服务。
