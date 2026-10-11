// 邮箱页：P1 只读快照（GET /api/characters/{id}/mailbox）
//        + P2 单封/批量删除（POST /api/characters/{id}/mailbox/delete）。
//
// 删除语义（与上游 0.1.8 dnfbridge/gm.go:516-596 对拍）：不带 force 时，服务端会
// 拒绝"还有未领取金币/附件"的邮件并如实回报是哪几封（响应 protected 字段），整批
// 零写入；勾选「强制删除」才连附件一起删。整箱清空仍然是「背包」页的既有按钮
// （POST …/mailbox/clear），这里不改它。

let mailboxEpoch = 0;
let mailboxDeleting = false;

function mailboxTime(value) {
  const seconds = Number(value);
  return Number.isFinite(seconds) && seconds > 0 ? escapeHtml(formatServerTime(seconds)) : '<span class="hint">—</span>';
}

function mailboxAttachmentsHtml(attachments) {
  if (!Array.isArray(attachments) || attachments.length === 0) return '<span class="hint">—</span>';
  return attachments.map((attachment) => {
    const name = String(attachment.name || '').trim();
    const label = name ? `${escapeHtml(name)}` : `<span class="hint">名称 PVF 未提供</span>`;
    const bind = attachment.bind ? ' <span class="p1-flag">绑定</span>' : '';
    const expire = Number(attachment.expireAt) > 0
      ? ` <span class="hint">至 ${escapeHtml(formatServerTime(attachment.expireAt))}</span>` : '';
    return `<div>#${attachment.itemId} ${label} ×${Number(attachment.count).toLocaleString()}${bind}${expire}</div>`;
  }).join('');
}

// mailboxSelectedIds 读当前勾选的邮件号（值就是快照里的 mailId 字符串）。
function mailboxSelectedIds() {
  return Array.from(document.querySelectorAll('#mailbox-body input.mailbox-select:checked'))
    .map((input) => String(input.value));
}

function updateMailboxDeleteButton() {
  const button = $('#btn-mailbox-delete');
  if (!button) return;
  const selected = mailboxSelectedIds().length;
  button.disabled = mailboxDeleting || !currentChar || selected === 0;
  button.textContent = mailboxDeleting ? '删除中…' : (selected > 0 ? `删除选中 ${selected} 封` : '删除选中邮件');
}

async function loadMailboxTab() {
  if (!currentChar) return;
  const epoch = ++mailboxEpoch;
  const body = $('#mailbox-body');
  body.innerHTML = '<div class="hint">加载中…</div>';
  const characterId = currentChar.characterId;
  try {
    const data = await api(`/api/characters/${characterId}/mailbox`);
    if (epoch !== mailboxEpoch || !currentChar || currentChar.characterId !== characterId) return;
    renderMailbox(data);
  } catch (e) {
    if (epoch !== mailboxEpoch) return;
    body.innerHTML = `<div class="card"><div class="hint err">${escapeHtml(e.message)}</div></div>`;
  }
}

async function deleteSelectedMailboxMails() {
  if (mailboxDeleting || !currentChar) return;
  const characterId = Number(currentChar.characterId);
  if (!Number.isSafeInteger(characterId) || characterId <= 0) {
    toast('当前角色无有效 ID', true);
    return;
  }
  const mailIds = mailboxSelectedIds();
  if (mailIds.length === 0) return;
  if (mailIds.length > 100) {
    toast('一次最多删除 100 封，请分批操作', true);
    return;
  }
  const forceInput = $('#mailbox-delete-force');
  const force = !!(forceInput && forceInput.checked);
  const forceNote = force ? '（强制：连未领取的附件/金币一起删）' : '';
  if (!confirm(`确认从当前角色邮箱删除 ${mailIds.length} 封邮件${forceNote}？`)) return;

  mailboxDeleting = true;
  updateMailboxDeleteButton();
  try {
    const result = await post(
      `/api/characters/${encodeURIComponent(String(characterId))}/mailbox/delete`,
      { mailIds, force });
    const deleted = Number(result.deleted || 0);
    const already = Array.isArray(result.alreadyDeleted) ? result.alreadyDeleted.length : 0;
    const note = already > 0 ? `；另有 ${already} 封此前已删除（无操作）` : '';
    toast(`已删除 ${deleted} 封邮件${note}`);
  } catch (e) {
    toast(`删除邮件失败：${e.message}`, true);
  } finally {
    mailboxDeleting = false;
    await loadMailboxTab();
  }
}

function renderMailbox(data) {
  const mails = data.mails || [];
  const summary = `<div class="card">
    <div class="card-title">邮箱 <span class="hint">共 ${data.count} 封 · 未读 ${data.unreadCount} · 可领取 ${data.claimableCount} · 附件 ${data.attachmentCount}</span></div>
    <div class="hint">只读快照 + 单封删除（≤100 封/次）。未带「强制删除」时，还有未领取金币/附件的邮件会被服务端拒绝并列出是哪几封；清空整箱仍在「背包」页用既有按钮。</div>
  </div>`;
  if (mails.length === 0) {
    $('#mailbox-body').innerHTML = summary + '<div class="card"><div class="hint">邮箱为空。</div></div>';
    updateMailboxDeleteButton();
    return;
  }
  const rows = mails.map((mail) => {
    const flags = [
      mail.claimed ? '<span class="p1-flag">已领取</span>' : '',
      mail.claimable ? '<span class="p1-flag">可领取</span>' : '',
      mail.read ? '<span class="p1-flag">已读</span>' : '<span class="p1-flag">未读</span>',
      mail.saved ? '<span class="p1-flag">已收藏</span>' : '',
    ].filter(Boolean).join(' ');
    const mailId = String(mail.mailId ?? '');
    return `<tr>
      <td><input type="checkbox" class="mailbox-select" value="${escapeHtml(mailId)}"></td>
      <td>${mailboxTime(mail.createdAt)}</td>
      <td>${escapeHtml(mail.title || '')}<div class="hint">${escapeHtml(mailId)}</div></td>
      <td>${escapeHtml(mail.senderName || '')}</td>
      <td>${Number(mail.gold || 0).toLocaleString()}</td>
      <td>${mailboxAttachmentsHtml(mail.attachments)}</td>
      <td>${flags}</td>
      <td>${mailboxTime(mail.expireAt)}</td>
    </tr>`;
  }).join('');
  $('#mailbox-body').innerHTML = summary + `<div class="card">
    <table>
      <thead><tr><th>选择</th><th>时间</th><th>标题</th><th>发件人</th><th>金币</th><th>附件</th><th>状态</th><th>过期</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
  </div>`;
  updateMailboxDeleteButton();
}
