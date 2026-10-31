# 变更记录与发布说明规范

自本规范合入后，每项改动使用独立变更记录。历史文件名仍为 `changlog.md`，保留原内容，普通 MR 不再修改它。详细技术方案与简短变更记录各司其职。

## 开发与评审

1. 开始任务时阅读近期 `changelog.d/` 记录和相关方案；历史背景再查 `changlog.md`。私有派生仓库同时读取其私有记录目录。
2. 在自己的分支新增 `changelog.d/YYYYMMDD-唯一事项.类型.md`；格式见[目录模板](../changelog.d/README.md)。类型为 fix/feature/change/docs，公开分类为“功能修复”。同一天不同事项必须不同文件。
3. 记录用户可观察变化、必要依赖、已做验证和待验收项。不要把源码合并写成已部署。详细分析链接到公开 Docs，原始证据留私有目录。
4. 同一个 MR 内可继续修改本项新增文件；一旦合并，该文件保持不变，纠正或补充另建条目并引用原事项。类型不同但事项 ID 相同也视为重复。
5. 相对目标基线的非空贡献须有新增记录。没有行为变化的文档/CI/测试改动使用 docs/change，简述实际影响；不使用随意跳过标签绕过检查。

群服内容只进入私有派生仓库的 `.pos5a3/changelog.d/`，分类“群服内容”。公共与私有记录使用同一格式和全局唯一事项 ID。公开检查和公开发布汇总只读取 `changelog.d/`；私有汇总可同时读取两个目录。公开分支不得携带私有目录。

## 本地检查与 CI

Python 3.10 或更新版本即可，无额外依赖。先暂存新增记录，再运行：

```text
python scripts/ci/changelog_fragments.py check --base <目标提交SHA>
python -m unittest discover -s scripts/ci -p test_changelog_fragments.py -v
```

GitLab MR CI 使用 `CI_MERGE_REQUEST_DIFF_BASE_SHA`，普通 push 使用非零 `CI_COMMIT_BEFORE_SHA`；初始分支没有比较基线时校验全部片段。缺少比较提交时失败，应完整获取历史，不静默跳过。输出 `dist/changelog-preview/release-notes.md` 与 `manifest.json` 为 CI artifact，不提交回主干。公开 GitHub 保留手动工作流入口，镜像职责保持。

私有 PR 使用 `check --scope group --base <私有目标SHA>`，要求本 PR 有新增群服记录；私有同步后的完整检查使用 `--scope all`。公开修复经公开 MR 合并后同步到私有，不能为了通过私有检查把公开修复改标为群服内容。

## 发布汇总

从固定的上次发布提交或 tag 到本次确定提交生成：

```text
python scripts/ci/changelog_fragments.py render --since <上次发布ref> --until <本次ref> --output-dir dist/release-notes-本次版本
```

省略 `--since` 会汇总所有片段，适合预览。私有群服发布追加 `--scope all`，输出仅保留在私有 artifact 或发布准备目录。工具核对祖先关系，拒绝已发布片段被改写/删除；按事项 ID 稳定排序，manifest 固定起止 SHA、片段路径、分类、类型和摘要。发布者审阅汇总后按既有发行流程使用，不覆盖已有输出。

汇总只覆盖采用新规范后的片段，老版本变化仍查历史 changlog。保留片段，不在发布后批量删除或修改它们；每次用准确的上次发布 ref 排除已包含内容。不要每合并一个 MR 就回写共享汇总文件。工作流只生成文件，不创建 Release、不推送源码、不部署服务。

## 旧分支的过渡处理

旧 MR 更新到新主干时，把本项新增 changlog 文字迁移为独立片段，保留目标分支历史；此后按新检查提交。新规范启用后的目标基线不接受普通贡献改写 `changlog.md`。

唯一冲突为旧 `changlog.md` 时，可先运行：

```text
python scripts/ci/changelog_fragments.py resolve-legacy
python scripts/ci/changelog_fragments.py resolve-legacy --apply
```

工具要求正在进行 merge，索引只有该文件冲突，双方文件均由“完整的新日期标题段落 + 逐字节不变的共同历史”组成。同名同文去重；同名异文、历史编辑/删除/重排、其他文件冲突、异常文件类型或残缺段落都停止。通过时只写入并暂存该文件，核对其他索引条目不变，输出摘要回执；提交与推送仍由调用者负责，不强推。

私有同步 CI 可调用其**已检出且经审查**的处理器处理这一窄场景，其他冲突继续创建私有 Issue。不得执行本次抓取来的新脚本，也不配置全局 `merge=union`。公开 MR 的自动写入不使用新增高权限机器人凭据；需要时由 Agent 调用上述工具后更新原 MR。
