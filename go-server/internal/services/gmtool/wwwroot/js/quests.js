// 任务页：P1 的只读查询面 + P5 的写面（接取/放弃/交付/完成/反完成/批量完成）
// + 批次 3 项 ⑥ 的「已接任务逐条/批量完成」。
//
//   GET  /api/characters/{id}/quests                                  总览（进行中 + 已完成）
//   GET  /api/characters/{id}/quests/search?q=&limit=
//   GET  /api/characters/{id}/quests/{questId}/detail
//   POST /api/characters/{id}/quests/{questId}/accept|give-up|ready|complete|uncomplete   （P5）
//   POST /api/characters/{id}/quests/complete-batch                                       （P5）
//   POST /api/characters/{id}/quests/active/complete-batch                                （批次 3 ⑥）
//
// 读取面的任务名与三段展示文本都直接来自 PVF；缺段时服务端返回空串，
// 这里显示「PVF 未提供」，**不**用路径以外的任何东西补名字。
// 写面的七条路由由服务端保证"被拒绝 = 零写入"，失败原因原样显示给操作者。
//
// 新控件的渲染条件是**服务端广告位**（GET …/quests 的 `completionBatch` 字段，
// 见 api_quest_active_batch.go）：该开关（那个 questModeEnv 环境变量）关闭档下服务端
// 不回这个字段，本文件于是不渲染逐条「强制完成」按钮、勾选框与两个批量按钮 ——
// 面板与加这条路由之前逐字节相同。前端**不读环境变量**（P4b 已定下的口径：脚本不探测
// 开关，只认服务端回来的能力位；TestQuestActiveBatchFrontendIsWired 会拦住开关名出现）。

let questLogEpoch = 0;
let questDetailEpoch = 0;
// questCompletionBatch 非 null = 服务端开启了「已接任务批量完成」（含新 UI 的渲染条件）。
let questCompletionBatch = null;

function questDisplayName(row) {
  const name = String(row.name || '').trim();
  if (name) return escapeHtml(name);
  const path = String(row.path || '').trim();
  if (path) return `<span class="p1-missing">PVF 未提供任务名</span> <span class="hint">${escapeHtml(path)}</span>`;
  return `<span class="p1-missing">PVF 未提供（该任务不在当前 Script.pvf）</span>`;
}

function questFlags(row) {
  if (row.active) return '<span class="p1-flag">进行中</span>';
  if (row.completed) return '<span class="p1-flag">已完成</span>';
  if (row.acceptable) return '<span class="p1-flag">可接取</span>';
  return `<span class="hint">${escapeHtml(row.status || '')}</span>`;
}

function questRowHtml(row, options) {
  const opts = options || {};
  // 两个开关都来自服务端广告位：selectable = 勾选框列，actions = 行内「强制完成」按钮。
  const pickable = opts.selectable === true;
  const lead = pickable
    ? `<td><input type="checkbox" class="questlog-active-pick" data-quest-id="${row.questId}"`
      + `${row.active === true ? '' : ' disabled'}></td>`
    : '';
  const actions = [];
  if (opts.actions === true && row.active === true) {
    // 逐条完成走的是**既有**路由（P5 的 POST …/quests/{questId}/complete），
    // 不是本批新增的那条：单条强制完成的语义与上游 ForceCompleteQuest 一致（不发奖励）。
    actions.push(`<button class="mini" data-quest-action="complete" data-quest-id="${row.questId}"`
      + ` title="GM 强制完成：不发奖励（gm_rewards_granted=false）；职业任务默认被拒绝">强制完成</button>`);
  }
  actions.push(`<button class="mini" data-quest-id="${row.questId}">详情</button>`);
  return `<tr>
    ${lead}
    <td>${questFlags(row)}</td>
    <td>${row.levelMin === row.levelMax ? row.levelMin : `${row.levelMin}~${row.levelMax}`}</td>
    <td>${row.questId}</td>
    <td>${questDisplayName(row)}</td>
    <td>${escapeHtml(row.grade || '')} ${escapeHtml(row.type || '')}</td>
    <td>${actions.join(' ')}</td>
  </tr>`;
}

function bindQuestDetailButtons(root) {
  root.querySelectorAll('button[data-quest-id]').forEach((button) => {
    button.onclick = () => showQuestDetail(Number(button.dataset.questId));
  });
}

// bindQuestRowActions 必须在 bindQuestDetailButtons **之后**调用：行内的
// 「强制完成」按钮同时带 data-quest-id（为了复用详情按钮的取号方式），这里用
// 赋值 onclick 覆盖掉刚绑上的详情动作 —— 顺序反了就会"点完成却打开详情"。
function bindQuestRowActions(root) {
  root.querySelectorAll('button[data-quest-action]').forEach((button) => {
    const questId = Number(button.dataset.questId);
    const action = button.dataset.questAction;
    button.onclick = () => runQuestMutation(questId, action);
  });
}

async function loadQuestLogTab() {
  if (!currentChar) return;
  const epoch = ++questLogEpoch;
  const body = $('#questlog-body');
  body.innerHTML = '<div class="hint">加载中…</div>';
  // 切角色瞬间先收起上一角色的批量控件（新广告位要等这次响应回来才作数）。
  questCompletionBatch = null;
  $('#questlog-active-actions').classList.add('hidden');
  const characterId = currentChar.characterId;
  try {
    const data = await api(`/api/characters/${characterId}/quests`);
    if (epoch !== questLogEpoch || !currentChar || currentChar.characterId !== characterId) return;
    renderQuestOverview(data);
  } catch (e) {
    if (epoch !== questLogEpoch) return;
    body.innerHTML = `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
  }
}

function renderQuestOverview(data) {
  // 服务端广告位：null/缺失 = 开关关闭档 → 一行新控件都不渲染（回退档面板与今天相同）。
  questCompletionBatch = data.completionBatch || null;
  const selectable = questCompletionBatch !== null;
  const section = (title, rows, options) => {
    const opts = options || {};
    const head = opts.selectable ? '<th></th>' : '';
    const all = opts.selectable
      ? ' <label class="hint"><input type="checkbox" id="questlog-active-all"> 全选</label>' : '';
    if (!rows.length) return `<div class="card"><div class="card-title">${title}${all}</div><div class="hint">没有记录。</div></div>`;
    return `<div class="card">
      <div class="card-title">${title}${all} <span class="hint">${rows.length} 条</span></div>
      <table>
        <thead><tr>${head}<th>状态</th><th>Lv</th><th>ID</th><th>名称</th><th>类型</th><th></th></tr></thead>
        <tbody>${rows.map((row) => questRowHtml(row, opts)).join('')}</tbody>
      </table>
    </div>`;
  };
  const active = data.active || [];
  $('#questlog-body').innerHTML =
    section('进行中', active, { selectable, actions: selectable })
    + section('已完成', data.completed || []);
  bindQuestDetailButtons($('#questlog-body'));
  bindQuestRowActions($('#questlog-body'));
  bindQuestActivePicks();
  // 只有「开关启用 + 当前确实有已接任务」时才把批量控件显示出来。
  $('#questlog-active-actions').classList.toggle('hidden', !(selectable && active.length > 0));
  updateQuestActiveSelectionHint();
}

// bindQuestActivePicks 绑定勾选框与「全选」：勾选状态只存在 DOM 里（不跨刷新保留），
// 每次勾选后更新提示行里的条数。
function bindQuestActivePicks() {
  document.querySelectorAll('#questlog-body input.questlog-active-pick').forEach((box) => {
    box.onchange = updateQuestActiveSelectionHint;
  });
  const all = $('#questlog-active-all');
  if (all) {
    all.checked = false;
    all.onchange = () => {
      document.querySelectorAll('#questlog-body input.questlog-active-pick:not([disabled])')
        .forEach((box) => { box.checked = all.checked; });
      updateQuestActiveSelectionHint();
    };
  }
}

function questActiveSelection() {
  return Array.from(document.querySelectorAll('#questlog-body input.questlog-active-pick:checked'))
    .map((box) => Number(box.dataset.questId))
    .filter((id) => Number.isInteger(id) && id > 0);
}

function updateQuestActiveSelectionHint() {
  const state = $('#questlog-active-state');
  if (!state) return;
  if (!questCompletionBatch) {
    state.textContent = '';
    return;
  }
  const picked = questActiveSelection().length;
  state.textContent = `已选 ${picked} 条。「完成选中的已接任务」只提交勾选的编号；`
    + '「完成全部已接任务」不提交编号，由服务端按角色任务账派生当前已接集合。'
    + '两者都不发奖励，每日任务与职业任务会被跳过并逐条回报原因。';
}


async function searchQuestLibrary() {
  if (!currentChar) return;
  const box = $('#questlog-search-results');
  const query = $('#questlog-query').value.trim();
  const epoch = ++questLogEpoch;
  const characterId = currentChar.characterId;
  box.innerHTML = '<div class="hint">搜索中…</div>';
  try {
    const data = await api(`/api/characters/${characterId}/quests/search?limit=50&q=` + encodeURIComponent(query));
    if (epoch !== questLogEpoch || !currentChar || currentChar.characterId !== characterId) return;
    const results = data.results || [];
    if (results.length === 0) {
      box.innerHTML = `<div class="card"><div class="hint">没有匹配的任务（搜索按任务名与 PVF 路径；直接输入任务 ID 可精确命中）。</div></div>`;
      return;
    }
    box.innerHTML = `<div class="card">
      <div class="card-title">搜索结果 <span class="hint">${results.length} 条</span></div>
      <table>
        <thead><tr><th>状态</th><th>Lv</th><th>ID</th><th>名称</th><th>类型</th><th></th></tr></thead>
        <tbody>${results.map(questRowHtml).join('')}</tbody>
      </table>
    </div>`;
    bindQuestDetailButtons(box);
  } catch (e) {
    if (epoch !== questLogEpoch) return;
    box.innerHTML = `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
  }
}

async function showQuestDetail(questId) {
  if (!currentChar) return;
  const epoch = ++questDetailEpoch;
  const box = $('#questlog-detail');
  box.innerHTML = '<div class="card"><div class="hint">加载中…</div></div>';
  const characterId = currentChar.characterId;
  try {
    const data = await api(`/api/characters/${characterId}/quests/${questId}/detail`);
    if (epoch !== questDetailEpoch || !currentChar || currentChar.characterId !== characterId) return;
    renderQuestDetail(data);
  } catch (e) {
    if (epoch !== questDetailEpoch) return;
    box.innerHTML = `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
  }
}

function questRewardHtml(items, title) {
  if (!Array.isArray(items) || items.length === 0) return '';
  const rows = items.map((item) => {
    const name = String(item.name || '').trim();
    const label = name ? escapeHtml(name) : `<span class="p1-missing">名称 PVF 未提供</span>`;
    const jobFilter = item.hasJobFilter
      ? ` <span class="p1-flag">职业 ${item.requiredJob} / 成长 ${item.requiredGrowType}</span>` : '';
    const selectable = item.selectable ? ' <span class="p1-flag">可选</span>' : '';
    return `<tr><td>${item.itemId}</td><td>${label}</td><td>${Number(item.count).toLocaleString()}</td><td>${selectable}${jobFilter}</td></tr>`;
  }).join('');
  return `<div class="card">
    <div class="card-title">${title}</div>
    <table><thead><tr><th>ID</th><th>名称</th><th>数量</th><th></th></tr></thead><tbody>${rows}</tbody></table>
  </div>`;
}

function renderQuestDetail(data) {
  const quest = data.quest || {};
  const parts = [];
  parts.push(`<div class="card">
    <div class="card-title">${questDisplayName(quest)} <span class="hint">#${quest.questId}</span></div>
    ${p1Kv([
      ['状态', escapeHtml(quest.status || '')],
      ['可接取', quest.acceptable ? '是' : '否'],
      ['类型', escapeHtml(`${quest.grade || ''} ${quest.type || ''}`.trim())],
      ['等级窗口', `${quest.levelMin} ~ ${quest.levelMax}`],
      ['PVF 路径', p1Text(quest.path)],
      ['前置任务', (quest.prerequisites || []).map((group) => `[${group.join(', ')}]`).join(' ') || p1Missing()],
      ['主任务 ID', quest.mainQuestId > 0 ? String(quest.mainQuestId) : p1Missing()],
      ['不可放弃', quest.cantGiveUp ? '是' : '否'],
      ['奖励类型', p1Text(quest.rewardType)],
      ['职业任务', quest.professionQuest ? '是' : '否'],
      ['难度', p1Text(data.difficulty)],
      ['限定职业', p1Text(data.job)],
    ])}
  </div>`);
  parts.push(`<div class="card">
    <div class="card-title">任务文本 <span class="hint">全部取自 PVF；缺段显示「PVF 未提供」</span></div>
    <div class="row"><span class="hint">介绍 depend message</span></div>${p1LongText(data.introduction)}
    <div class="row"><span class="hint">条件 condition message</span></div>${p1LongText(data.objective)}
    <div class="row"><span class="hint">结算 solve message</span></div>${p1LongText(data.completion)}
  </div>`);
  parts.push(questRewardHtml(data.rewardItems, '奖励物品') + questRewardHtml(data.rewardSelectionItems, '可选奖励'));
  parts.push(questMutationButtons(quest));
  const box = $('#questlog-detail');
  box.innerHTML = parts.join('');
  // 动态生成的按钮就地绑定（renderQuestDetail 每次整块重建 innerHTML）。
  box.querySelectorAll('button[data-quest-action]').forEach((button) => {
    button.onclick = () => runQuestMutation(Number(button.dataset.questId), button.dataset.questAction);
  });
}

// ---------------------------------------------------------------------------
// P5 任务写面（J9 接取/放弃、J10 交付/完成/反完成、J11 批量完成）
//
// 六条路由与后端 internal/services/gmtool/api_quest_mutations.go 一一对应：
//   POST /api/characters/{id}/quests/{questId}/accept|give-up|ready|complete|uncomplete
//   POST /api/characters/{id}/quests/complete-batch
// （批次 3 ⑥ 的第七条 POST …/quests/active/complete-batch 在下面的
//  「已接任务逐条 / 批量完成」一节，后端在 api_quest_active_batch.go。）
//
// 服务端恒 HTTP 200 + success:false 报业务失败（本仓契约，不引入 409），
// 因此这里只需要把 e.message 原样显示 —— 被拒绝的原因由服务端给出中文说明。
// ---------------------------------------------------------------------------

const QUEST_MUTATION_LABELS = {
  'accept': '接取',
  'give-up': '放弃',
  'ready': '标记可交付',
  'complete': '强制完成',
  'uncomplete': '取消完成',
};

function questMutationPath(questId, action) {
  return `/api/characters/${currentChar.characterId}/quests/${questId}/${action}`;
}

// questMutationButtons 按任务当前状态决定哪些操作可用；不可用的按钮带 title 说明原因，
// 但**不隐藏** —— 操作者要能看到"这个任务为什么不能这么做"。
function questMutationButtons(quest) {
  const questId = Number(quest.questId);
  const active = quest.active === true;
  const completed = quest.completed === true;
  const mainQuestId = Number(quest.mainQuestId) || 0;
  const entries = [
    ['accept', quest.acceptable === true && !active && !completed,
      quest.professionQuest ? '职业任务接取不受职业写开关限制，但仍要满足 PVF 条件'
        : '按 PVF 条件接取（复用服务端任务 owner，不发奖励）'],
    ['give-up', active && quest.cantGiveUp !== true,
      quest.cantGiveUp ? 'PVF 标记为不可放弃（[cant giveup]）' : '删掉进行中的任务行（带资产的任务会被 owner 拒绝）'],
    ['ready', active, '进度归零并清掉结算回执，交给玩家自己去交付'],
    ['complete', !completed, 'GM 强制完成：不发奖励（gm_rewards_granted=false）；职业任务默认被拒绝'],
    ['uncomplete', completed && mainQuestId === 0,
      mainQuestId > 0 ? `主线链路任务（main quest ${mainQuestId}）：整链重置尚未移植，服务端会拒绝`
        : '从 States 与 Progress 两张表删掉完成记录'],
  ];
  const buttons = entries.map(([action, enabled, title]) =>
    `<button class="mini" data-quest-action="${action}" data-quest-id="${questId}"`
    + `${enabled ? '' : ' disabled'} title="${escapeHtml(title)}">${QUEST_MUTATION_LABELS[action]}</button>`).join('');
  return `<div class="card">
    <div class="card-title">任务写操作 <span class="hint">六条新增路由；总开关 DNFBRIDGE_GM_QUEST_MUTATE 为空即启用</span></div>
    <div class="row">${buttons}</div>
    <div class="hint">被拒绝时服务端不会写任何字节，并给出原因（PVF 里没有 / 状态不对 / 职业任务开关 / 主线链路 / 角色在线）。</div>
  </div>`;
}

async function runQuestMutation(questId, action) {
  if (!currentChar) return;
  const label = QUEST_MUTATION_LABELS[action] || action;
  if (!window.confirm(`确认对任务 ${questId} 执行「${label}」？`)) return;
  const characterId = currentChar.characterId;
  try {
    const data = await post(questMutationPath(questId, action), {});
    const notes = [];
    if (data.changed === false) notes.push('无变化（幂等）');
    if (data.alreadyCompleted === true) notes.push('任务本来就是完成态，未改动任何字节');
    if (data.professionChanged === true) notes.push(`职业已跃迁，grow_type=${data.growType}`);
    if (Array.isArray(data.clearedKeys) && data.clearedKeys.length > 0) {
      notes.push(`清掉结算回执：${data.clearedKeys.join(', ')}`);
    }
    toast(`任务 ${questId} ${label}成功${notes.length ? '（' + notes.join('；') + '）' : ''}`);
    if (!currentChar || currentChar.characterId !== characterId) return;
    await loadQuestLogTab();
    await showQuestDetail(questId);
  } catch (e) {
    toast(`${label}任务 ${questId} 失败：${e.message}`, true);
  }
}

async function completeBatchQuests() {
  if (!currentChar) return;
  const raw = $('#questlog-batch-ids').value.trim();
  const ids = raw.split(/[\s,;]+/).filter((part) => part.length > 0).map((part) => Number(part));
  if (ids.length === 0 || ids.some((id) => !Number.isInteger(id) || id <= 0 || id > 65535)) {
    toast('请填写 1..65535 的任务 ID（逗号或空格分隔）', true);
    return;
  }
  if (ids.length > 200) {
    toast('一次最多 200 个任务 ID（服务端硬上限）', true);
    return;
  }
  if (!window.confirm(`确认批量完成 ${ids.length} 个任务？服务端整批校验：任一条不合格则整批拒绝、零写入。`)) return;
  const data = await postQuestBatchJson(
    `/api/characters/${currentChar.characterId}/quests/complete-batch`, { questIds: ids });
  if (!data) return;
  renderQuestBatchResult('批量完成', data);
  if (data.success === true) {
    toast(`批量完成：写入 ${data.changed} 条，未变 ${data.unchanged} 条`);
    if (!currentChar) return;
    await loadQuestLogTab();
    return;
  }
  toast('批量完成失败：' + String(data.error || '服务端拒绝'), true);
}

// ---------------------------------------------------------------------------
// 批次 3 项 ⑥：已接任务逐条 / 批量完成（对齐上游 A21 GM 工具）
//
//   POST /api/characters/{id}/quests/active/complete-batch     （api_quest_active_batch.go）
//
// 请求体 {"questIds":[…]} 缺省 = 由服务端按角色任务账派生「当前已接」集合。
// 服务端逐条回报 outcome（completed / already_completed / rejected）与 reason
// （not_accepted / daily_excluded / profession_excluded / not_in_script_pvf …），
// 因此这里必须用原生 fetch 保留整份对账信息（api() 只抛 error 文案）。
// 渲染条件由 renderQuestOverview 收到的 completionBatch 广告位决定（开关关闭档下
// 两个按钮与勾选框根本不会渲染）。
// ---------------------------------------------------------------------------

// postQuestBatchJson 打一次批量写请求并返回响应体；请求本身失败时返回 null
// （错误已经写进 #questlog-batch-result 并 toast 过）。
async function postQuestBatchJson(path, body) {
  const characterId = currentChar ? currentChar.characterId : null;
  let response;
  try {
    response = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    });
  } catch (e) {
    $('#questlog-batch-result').innerHTML =
      `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
    toast('请求失败：' + e.message, true);
    return null;
  }
  let data;
  try {
    data = await response.json();
  } catch (e) {
    toast('响应无法解析：' + e.message, true);
    return null;
  }
  if (data && data.loginRequired === true && typeof handleAuthenticationRequired === 'function') {
    handleAuthenticationRequired();
    return null;
  }
  // 切角色后到达的旧响应：不写 DOM、不提示，避免把上一个角色的结果画到新角色页上。
  if (!currentChar || currentChar.characterId !== characterId) return null;
  return data;
}

// renderQuestBatchResult 是两条批量完成路由**共用**的结果渲染（不写第二套）。
// heading 是卡片标题前缀（"批量完成" / "完成已接任务"）。
function renderQuestBatchResult(heading, data) {
  const results = Array.isArray(data.results) ? data.results : [];
  const problems = Array.isArray(data.problems) ? data.problems : [];
  const lines = results.map((row) => {
    const name = String(row.name || '').trim();
    const reason = row.reason ? `（${row.reason}）` : '';
    return `#${row.questId} ${row.outcome}${reason}${name ? ' ' + name : ''}`;
  });
  if (problems.length > 0) {
    lines.push(`不合格：${problems.map((row) => `#${row.questId}（${row.reason}）`).join('、')}`);
  }
  const title = data.success === true
    ? `${heading} <span class="hint">请求 ${data.requested} / 唯一 ${data.unique} / 写入 ${data.changed} / 未变 ${data.unchanged}`
      + `${data.derived === true ? ' / 服务端按任务账派生' : ''}</span>`
    : `${heading}被拒绝 <span class="hint">整批未做任何写入</span>`;
  const body = lines.length > 0
    ? lines.map((line) => escapeHtml(line)).join('<br>')
    : escapeHtml(String(data.error || ''));
  $('#questlog-batch-result').innerHTML =
    `<div class="card"><div class="card-title">${title}</div><div class="hint">${body}</div></div>`;
}

// questActiveBatchPath 只在服务端给了广告位时才可用（questCompletionBatch 为 null
// 时两个入口都在 UI 上被隐藏；这里再兜一次，防止旧按钮被程序化点到）。
function questActiveBatchPath() {
  if (!questCompletionBatch || !currentChar) return null;
  return `/api/characters/${currentChar.characterId}/quests/active/complete-batch`;
}

async function completeSelectedActiveQuests() {
  const path = questActiveBatchPath();
  if (!path) return;
  const ids = questActiveSelection();
  if (ids.length === 0) {
    toast('请先勾选要完成的已接任务（或用「完成全部已接任务」）', true);
    return;
  }
  if (!window.confirm(`确认强制完成勾选的 ${ids.length} 个已接任务？不发奖励；`
    + '未接过的编号会被拒绝，每日/职业任务会被跳过并逐条回报。')) return;
  await runQuestActiveBatch(path, { questIds: ids }, `完成勾选的 ${ids.length} 个已接任务`);
}

async function completeAllActiveQuests() {
  const path = questActiveBatchPath();
  if (!path) return;
  if (!window.confirm('确认强制完成该角色当前**全部已接（进行中）**任务？'
    + '不发奖励；每日任务与职业任务会被跳过并逐条回报。')) return;
  // 不传 questIds = 让服务端按角色任务账派生（避免前端自己算「哪些算已接」的第二套口径）。
  await runQuestActiveBatch(path, {}, '完成全部已接任务');
}

async function runQuestActiveBatch(path, body, label) {
  const data = await postQuestBatchJson(path, body);
  if (!data) return;
  renderQuestBatchResult('完成已接任务', data);
  if (data.success === true) {
    const skipped = (data.skippedDaily || []).length + (data.skippedProfession || []).length;
    const notes = [`写入 ${data.changed} 条`, `未变 ${data.unchanged} 条`];
    if (skipped > 0) notes.push(`跳过每日/职业 ${skipped} 条`);
    if ((data.notAccepted || []).length > 0) notes.push(`未接拒绝 ${data.notAccepted.length} 条`);
    toast(`${label}：${notes.join('，')}`);
    await loadQuestLogTab();
    return;
  }
  toast(`${label}失败：` + String(data.error || '服务端拒绝'), true);
}

// refreshActiveReadonlyTab 在切换角色时被 sidebar.js 调用：只刷新当前**已激活**的
// 只读页，避免在别的标签页上白跑请求。
function refreshActiveReadonlyTab() {
  const active = document.querySelector('#detail .tab.active');
  if (!active) return;
  switch (active.dataset.tab) {
    case 'itemlookup':
      break; // 物品详情不依赖角色
    case 'mailbox':
      loadMailboxTab();
      break;
    case 'questlog':
      loadQuestLogTab();
      $('#questlog-detail').innerHTML = '';
      $('#questlog-search-results').innerHTML = '';
      $('#questlog-batch-result').innerHTML = '';
      // 批次 3 ⑥ 的批量控件：先收起（新角色的广告位要等这次总览响应回来才作数）。
      questCompletionBatch = null;
      $('#questlog-active-actions').classList.add('hidden');
      $('#questlog-active-state').textContent = '';
      break;
    default:
      break;
  }
}
