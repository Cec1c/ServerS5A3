let activityRevision = null;
let activityBusy = false;
let activityLoadEpoch = 0;

function renderActivities(snapshot) {
  activityRevision = snapshot.revision;
  const list = $('#activities-list');
  list.replaceChildren();
  for (const activity of snapshot.activities) {
    const row = document.createElement('div');
    row.className = 'card';
    const label = document.createElement('label');
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox'; checkbox.dataset.activityId = String(activity.id);
    checkbox.checked = activity.enabled;
    label.append(checkbox, document.createTextNode(' ' + activity.name));
    const description = document.createElement('p'); description.textContent = activity.description;
    const status = document.createElement('p'); status.className = 'hint';
    status.textContent = '当前生效：' + (activity.effectiveEnabled ? '开启' : '关闭')
      + (activity.pendingRestart ? ' · 已保存新设置，待服务端重启' : '') + ' · 活动编号 ' + activity.id;
    row.append(label, description, status); list.append(row);
  }
  $('#activities-state').textContent = snapshot.restartRequired ? '已保存，待服务端重启后生效' : '已与当前服务同步';
}

async function loadActivities() {
  if (activityBusy) return;
  const epoch = ++activityLoadEpoch;
  activityRevision = null;
  $('#btn-save-activities').disabled = true;
  $('#activities-state').textContent = '正在读取活动…';
  try {
    const data = await api('/api/activities');
    if (epoch !== activityLoadEpoch) return;
    renderActivities(data.activityControls);
    $('#btn-save-activities').disabled = false;
  } catch (error) {
    if (epoch === activityLoadEpoch) $('#activities-state').textContent = error.message;
  }
}

$('#btn-activities').addEventListener('click', () => {
  $('#activities-panel').classList.remove('hidden'); loadActivities();
});
$('#btn-close-activities').addEventListener('click', () => {
  ++activityLoadEpoch; $('#activities-panel').classList.add('hidden');
});
$('#btn-refresh-activities').addEventListener('click', loadActivities);
$('#btn-save-activities').addEventListener('click', async () => {
  if (activityBusy || activityRevision == null) return;
  activityBusy = true; ++activityLoadEpoch;
  $('#btn-save-activities').disabled = true; $('#btn-refresh-activities').disabled = true;
  const activities = Array.from(document.querySelectorAll('#activities-list input[data-activity-id]'))
    .map(input => ({id: Number(input.dataset.activityId), enabled: input.checked}));
  try {
    const data = await post('/api/activities', {revision: activityRevision, activities});
    renderActivities(data.activityControls); toast('活动设置已保存');
  } catch (error) {
    activityRevision = null; $('#activities-state').textContent = error.message; toast(error.message, true);
  } finally {
    activityBusy = false; $('#btn-save-activities').disabled = activityRevision == null;
    $('#btn-refresh-activities').disabled = false;
  }
});
