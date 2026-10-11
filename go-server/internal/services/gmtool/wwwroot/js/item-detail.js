// P1 只读查询面：物品详情（GET /api/items/{itemId}/detail）。
//
// 纪律：所有展示内容都来自 PVF。服务端在 PVF 缺段时**不返回该字段**（数值段）
// 或返回空串（文本段），这里统一渲染成「PVF 未提供」，不填任何默认文案。

const P1_MISSING_TEXT = 'PVF 未提供';

let itemDetailEpoch = 0;

function p1Missing() {
  return `<span class="p1-missing">${P1_MISSING_TEXT}</span>`;
}

function p1LongText(value) {
  const text = String(value === null || value === undefined ? '' : value).trim();
  if (!text) return p1Missing();
  return `<div class="p1-longtext">${escapeHtml(text).replace(/\n/g, '<br>')}</div>`;
}

function p1Number(value) {
  if (value === null || value === undefined) return p1Missing();
  return Number(value).toLocaleString();
}

function p1Text(value) {
  const text = String(value === null || value === undefined ? '' : value).trim();
  return text ? escapeHtml(text) : p1Missing();
}

function p1Kv(rows) {
  return `<div class="p1-kv">${rows.map(([k, v]) => `<div><span class="k">${escapeHtml(k)}</span><span class="v">${v}</span></div>`).join('')}</div>`;
}

function p1AttributeTable(attributes) {
  if (!Array.isArray(attributes) || attributes.length === 0) {
    return `<div class="hint">${P1_MISSING_TEXT}</div>`;
  }
  const rows = attributes.map((attribute) =>
    `<tr><td>${escapeHtml(attribute.name)}</td><td>${Number(attribute.value).toLocaleString()}</td></tr>`).join('');
  return `<table class="stats-table"><tbody>${rows}</tbody></table>`;
}

async function loadItemLookupTab() {
  // 物品详情不依赖角色，进入页面时把光标放到查询框即可。
  const input = $('#item-detail-query');
  if (input) input.focus();
}

async function loadItemLookupCandidates(query) {
  const box = $('#item-detail-candidates');
  const text = String(query || '').trim();
  if (!text) {
    box.innerHTML = '';
    return;
  }
  try {
    const data = await api('/api/items/search?limit=30&q=' + encodeURIComponent(text));
    const results = data.results || [];
    if (results.length === 0) {
      box.innerHTML = '<span class="hint">没有匹配的物品；可以直接输入物品 ID 查询详情。</span>';
      return;
    }
    box.innerHTML = results.map((item) =>
      `<button class="mini" data-item-id="${item.itemId}">#${item.itemId} ${escapeHtml(item.name)}</button>`).join('');
    box.querySelectorAll('button[data-item-id]').forEach((button) => {
      button.onclick = () => showItemDetail(Number(button.dataset.itemId));
    });
  } catch (e) {
    box.innerHTML = `<span class="hint err">${escapeHtml(e.message)}</span>`;
  }
}

async function queryItemDetail() {
  const raw = $('#item-detail-query').value.trim();
  if (!raw) return toast('请输入物品 ID 或物品名', true);
  if (/^\d+$/.test(raw)) {
    await showItemDetail(Number(raw));
    return;
  }
  await loadItemLookupCandidates(raw);
}

async function showItemDetail(itemId) {
  const epoch = ++itemDetailEpoch;
  const body = $('#item-detail-body');
  body.innerHTML = '<div class="hint">加载中…</div>';
  try {
    const data = await api('/api/items/' + itemId + '/detail');
    if (epoch !== itemDetailEpoch) return;
    renderItemDetail(data);
  } catch (e) {
    if (epoch !== itemDetailEpoch) return;
    body.innerHTML = `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
  }
}

function renderItemDetail(data) {
  const item = data.item || {};
  const name = String(item.name || '').trim();
  const header = name
    ? escapeHtml(name)
    : `<span class="p1-missing">${P1_MISSING_TEXT}物品名</span>`;

  const parts = [];
  parts.push(`<div class="card">
    <div class="card-title">${header} <span class="hint">#${item.itemId} · ${escapeHtml(item.kind || '')}</span></div>
    ${p1Kv([
      ['PVF 路径', p1Text(item.pvfPath)],
      ['类型串', p1Text(item.kind === 'stackable' ? item.stackableType : item.equipmentType)],
      ['品级 grade', p1Number(item.grade)],
      ['稀有度 rarity', p1Number(item.rarity)],
      ['最低等级', p1Number(item.minimumLevel)],
      ['堆叠上限', p1Number(item.stackLimit)],
      ['使用职业', p1Text(item.usableJob)],
      ['获得后期限(天)', p1Number(item.usablePeriodDays)],
      ['固定期限', item.expirationDate > 0 ? escapeHtml(formatServerTime(item.expirationDate)) : p1Missing()],
      ['归属类型', p1Text(item.attachType)],
      ['图标', item.iconPath ? `${escapeHtml(item.iconPath)} <span class="hint">#${item.iconIndex}</span>` : p1Missing()],
    ])}
  </div>`);

  parts.push(`<div class="card">
    <div class="card-title">文本段</div>
    <div class="row"><span class="hint">说明 explain</span></div>${p1LongText(data.explain)}
    <div class="row"><span class="hint">基本说明 basic explain</span></div>${p1LongText(data.basicExplain)}
    <div class="row"><span class="hint">详细说明 detail explain</span></div>${p1LongText(data.detailExplain)}
    <div class="row"><span class="hint">风味文本 flavor text</span></div>${p1LongText(data.flavorText)}
  </div>`);

  parts.push(`<div class="card">
    <div class="card-title">数值段</div>
    ${p1Kv([
      ['重量 weight', p1Number(data.weight)],
      ['修理费 repair price', p1Number(data.repairPrice)],
      ['价值 value', p1Number(data.value)],
      ['价格 price', p1Number(item.price)],
    ])}
    <div class="hint">这些段在 PVF 里没有时不显示数字，直接标「PVF 未提供」。</div>
  </div>`);

  if (item.kind === 'equipment') {
    const properties = Array.isArray(data.elementalProperties) && data.elementalProperties.length
      ? data.elementalProperties.map((value) => `<span class="p1-flag">${escapeHtml(value)}</span>`).join(' ')
      : `<span class="p1-missing">${P1_MISSING_TEXT}</span>`;
    parts.push(`<div class="card">
      <div class="card-title">装备属性
        <span class="hint">攻击速度 ${p1Text(data.weaponAttackSpeed)} · 品级基准 ${p1Number(data.qualityPercent)}%</span></div>
      <div class="row"><span class="hint">元素属性 elemental property</span>${properties}</div>
      <div class="row"><span class="hint">物品组名 item group name</span>${p1Text(data.itemGroupName)}</div>
      ${p1AttributeTable(data.attributes)}
      <div class="hint">攻防数值按「满品级」展示：PVF 基础段 × 1.1 后叠加 add 段（与上游 GM 面板同口径）。</div>
    </div>`);

    const set = data.equipmentSet;
    if (set) {
      const bonuses = (set.bonuses || []).map((bonus) => `
        <div class="card">
          <div class="card-title">${bonus.requiredPieces} 件套</div>
          <div class="row">${p1LongText(bonus.basicExplain)}</div>
          <div class="row">${p1LongText(bonus.detailExplain)}</div>
          ${p1AttributeTable(bonus.attributes)}
        </div>`).join('');
      parts.push(`<div class="card">
        <div class="card-title">套装 ${escapeHtml(set.name || '')} <span class="hint">setId ${set.setId} · ${escapeHtml(set.pvfPath || '')}</span></div>
        ${bonuses || `<div class="hint">${P1_MISSING_TEXT}（[piece set ability] 段缺失）</div>`}
      </div>`);
    } else if (data.partSetIndex > 0) {
      parts.push(`<div class="card"><div class="card-title">套装</div>
        <div class="hint">part set index = ${data.partSetIndex}，但 etc/equipmentpartset.etc 里没有这一项。</div></div>`);
    }
  }

  $('#item-detail-body').innerHTML = parts.join('');
}
