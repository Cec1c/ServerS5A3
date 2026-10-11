// P3 的三个运维动作的前端（J7 账号晶块 6 格 / J8 SP-TP 绝对值 / J16 账号金库金币）。
//
// 契约与其它页面一致：成功 200 + success:true，业务失败恒 200 + success:false
// （post/api 会把它转成 throw，这里统一 toast(e.message, true)）。
//
// 刻意**不**改动既有的「调整附加点」（增量）按钮与「账号货币」覆写行：
//   - 晶块是账号级数据（槽位 354-359），但上游路由与写入都要经一个角色来定位账号，
//     所以入口放在「背包」页（那里一定选着角色），而不是账号面板；
//   - SP/TP 绝对值是新卡片，与既有增量卡片并列，互不影响；
//   - 金库金币沿用账号面板既有的「覆写」行样式（见 sidebar.js 的 currencyDefs）。

async function loadAccountCubes() {
  if (!currentChar) return toast('请先选择一个角色', true);
  const epoch = selectEpoch;
  const body = $('#account-cube-table') ? $('#account-cube-table tbody') : null;
  if (!body) return;
  try {
    const data = await api(`/api/characters/${currentChar.characterId}/account-cubes`);
    if (epoch !== selectEpoch) return;
    renderAccountCubes(data);
  } catch (e) {
    body.innerHTML = `<tr><td colspan="7" class="hint">${escapeHtml(e.message)}</td></tr>`;
    const state = $('#account-cube-state');
    if (state) state.textContent = '';
  }
}

function renderAccountCubes(data) {
  const body = $('#account-cube-table').querySelector('tbody');
  const rows = Array.isArray(data.cubes) ? data.cubes : [];
  body.innerHTML = '';
  for (const cube of rows) {
    const tr = document.createElement('tr');
    tr.innerHTML =
      `<td>${Number(cube.selection) + 1}</td>` +
      `<td>${cube.slot}</td>` +
      `<td>${cube.itemId}</td>` +
      `<td>${escapeHtml(cube.name || '')}</td>` +
      `<td>${Number(cube.count).toLocaleString()}</td>` +
      `<td><input type="number" min="0" class="val-input" value="${Number(cube.count)}"></td>` +
      `<td><button class="mini" type="button">写入</button></td>`;
    tr.querySelector('button').onclick = () => writeAccountCube(cube, tr.querySelector('input'));
    body.appendChild(tr);
  }
  if (rows.length === 0) {
    body.innerHTML = '<tr><td colspan="7" class="hint">服务端没有返回晶块行</td></tr>';
  }
  const state = $('#account-cube-state');
  if (state) {
    state.textContent = `账号 ${data.accountId} 的晶块（写入 0 = 删除该格；改完让玩家重进场景生效）`;
  }
}

async function writeAccountCube(cube, input) {
  if (!currentChar) return toast('请先选择一个角色', true);
  const count = parseInt(input.value, 10);
  if (isNaN(count) || count < 0) return toast('请输入非负整数（0 = 删除该格）', true);
  const label = `${Number(cube.selection) + 1} 号格（${cube.name || cube.itemId}）`;
  const action = count === 0 ? `删除 ${label}` : `把 ${label} 写成 ${count}`;
  if (!window.confirm(`确认${action}？晶块是账号共享数据，写入立即生效。`)) return;
  try {
    await post(`/api/characters/${currentChar.characterId}/account-cubes`, {
      selection: Number(cube.selection),
      count,
    });
    toast(`已${count === 0 ? '删除' : '写入'} ${label}`);
    loadAccountCubes();
  } catch (e) {
    toast(e.message, true);
  }
}

async function setSpAbsoluteValues() {
  if (!currentChar) return toast('请先选择一个角色', true);
  const spInput = $('#sp-absolute-sp');
  const tpInput = $('#sp-absolute-tp');
  const sp = parseInt(spInput.value, 10);
  const tp = parseInt(tpInput.value, 10);
  if (isNaN(sp) || isNaN(tp)) return toast('附加 SP 与附加 TP 都必须填写（0..65535）', true);
  if (sp < 0 || tp < 0 || sp > 65535 || tp > 65535) return toast('附加 SP/TP 必须在 0..65535', true);
  if (!window.confirm(`确认把附加点设为 SP ${sp} / TP ${tp}？（这是覆盖，不是相加）`)) return;
  try {
    await post(`/api/characters/${currentChar.characterId}/sp/set`, { bonus_sp: sp, bonus_tp: tp });
    toast('附加点已设为绝对值');
    if (typeof loadSpTp === 'function') loadSpTp();
  } catch (e) {
    toast(e.message, true);
  }
}
