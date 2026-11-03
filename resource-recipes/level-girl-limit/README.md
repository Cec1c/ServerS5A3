# 练级少女商品按角色终身限购一次

本配方覆盖 490009269、490009270、490009271、490009274、490009275 五个既有商品。每份定义只把 [purchase limit] 后的 account 改为 character，保留 accumulate 和次数 1，其余 token 的类型与 payload 位型保持。

    python build_level_girl_limit.py --base <匹配manifest的Script.pvf> --pvfcli <匹配manifest的PVF-CLI> --tokens-dir <五份基线tokens.json所在目录> --output-dir <不存在的输出目录>

manifest 绑定基线、工具、schema 2 计划、目标索引与五个条目摘要。脚本在写入前验证范围；生成后核对 patch/verify 回执、五个条目的 token 读回及未修改条目完整性。五处修改复用同一个新增字符串 character。

已有候选可使用 --verify-existing <candidate> --patch-receipt <原patch.stdout>，并指定新的输出目录保存独立验哈结果。脚本不发布、不生成索引、不修改服务器数据库。配方使用历史冻结输入；其他累计输入需要重新审查绑定，不可跳过摘要检查。角色限购的服务端计数和游戏内购买行为应另行验收。
