// 90CN 面板作用域文件：A21 前端的任务 / 迁移 / 背包异常三个模块没有移植到
// 90CN 服务端，这里把它们的入口替换成安全的空实现，保证：
//   1) 其它已保留页面（发放 / 背包 / 角色属性 / 账号）不会因为缺少全局函数而报错；
//   2) 任何未移植的功能都不会发出请求，也不会显示成功假象。
(function () {
  const noop = () => {};
  const noopAsync = async () => {};

  // 任务页（90CN 未移植：任务写入涉及服务端任务 owner，不做只写一半的假实现）
  window.loadQuests = noopAsync;
  window.loadAllVisibleQuests = noopAsync;
  window.loadMainQuests = noopAsync;
  window.loadAchieveQuests = noopAsync;
  window.loadClearedQuests = noopAsync;
  window.searchQuestLib = noopAsync;
  window.setAllQuestDisplayMode = noop;
  window.completeCurrentLevelMainQuests = noopAsync;
  window.completeCurrentLevelMainQuestsFromAll = noopAsync;
  window.completeCurrentLevelSideQuests = noopAsync;
  window.completeCurrentLevelSideQuestsFromAll = noopAsync;
  window.completeCurrentLevelSystemQuests = noopAsync;
  window.completeCurrentLevelNoItemAchievementQuests = noopAsync;
  window.completeAllTitleBook = noopAsync;
  window.unclearCurrentTitleBookPage = noopAsync;
  window.resetDailyQuests = noopAsync;
  window.completeProfessionQuestsButton = noopAsync;
  window.completeEquipmentSlotQuests = noopAsync;
  window.confirmProfessionQuestChoice = noopAsync;
  window.closeProfessionQuestPanel = noop;
  window.openProfessionQuestPanel = noop;

  // 背包异常清理（A21 特有的库表维护，90CN 没有对应 schema）
  window.bindInventoryAnomalies = noop;
  window.refreshInventoryAnomalyStatus = noopAsync;
  window.resetInventoryAnomalyState = noop;

  // A12 → A21 迁移面板（90CN 不需要迁移，/api/status 也永远不报 migrationRequired）
  window.showA12A21DatabaseUnusable = noop;
  window.showA12A21MigrationRequired = noop;
  window.updateA12A21MigrationEnvironment = noop;
  window.bindA12A21Migration = noop;

  // 任务页容器整体隐藏（标签按钮已在 index.html 中移除，这里再兜底一次）
  document.addEventListener('DOMContentLoaded', () => {
    const page = document.getElementById('tab-quests');
    if (page) page.classList.add('hidden');
  });
})();
