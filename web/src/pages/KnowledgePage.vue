<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick } from "vue";
import { hasPerm } from "../auth.js";
import { api } from "../api.js";
import KnowledgeTreeNode from "../components/KnowledgeTreeNode.vue";
import KnowledgeVersionDialog from "../components/KnowledgeVersionDialog.vue";
import { notifyKnowledgeVersionChange, subscribeKnowledgeVersions } from "../knowledgeVersions.js";

const canManage = computed(() => hasPerm("knowledge:manage"));
const versions = ref([]), versionId = ref(""), defaultId = ref("");
const versionDialog = ref(null), versionsLoading = ref(false), versionsError = ref("");
let versionsRequestId = 0, unsubscribeVersions, disposed = false;
const currentVersion = computed(() => versions.value.find(v => v.id === versionId.value));
const tree = ref([]), expanded = ref(new Set()), selectedNode = ref(null), treeQuery = ref("");
const points = ref([]), total = ref(0), page = ref(1), pageSize = ref(30), query = ref(""), appliedQuery = ref("");
const loading = ref(true), treeLoading = ref(false), error = ref(""), notice = ref(""), busy = ref(false), exporting = ref(false);
const modal = ref(null), modalKind = ref(""), formError = ref(""), tableScroll = ref(null);
const pointForm = ref({}), versionForm = ref({}), importFiles = ref([]), importMode = ref("merge"), importResult = ref(null);
const deleteTarget = ref(null), detail = ref(null);
let requestId = 0, treeRequestId = 0, noticeTimer;
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)));
const selectedPath = computed(() => selectedNode.value?.path || []);
const contextName = computed(() => selectedNode.value?.label || "全部大纲要点");
const filteredTree = computed(() => {
  const q = treeQuery.value.trim().toLowerCase();
  if (!q) return tree.value;
  function filter(nodes) { return nodes.flatMap(node => { const children = filter(node.children); return node.label.toLowerCase().includes(q) ? [node] : children.length ? [{ ...node, children }] : []; }); }
  return filter(tree.value);
});
const visibleExpanded = computed(() => {
  if (!treeQuery.value.trim()) return expanded.value;
  const ids = new Set(); function visit(nodes) { nodes.forEach(n => { ids.add(n.id); visit(n.children); }); } visit(filteredTree.value); return ids;
});
const directoryOptions = computed(() => {
  const result = { category: new Set(), subject: new Set(), unit: new Set(), sub_item: new Set() };
  function visit(nodes) { nodes.forEach(n => { if (n.value) result[n.field].add(n.value); visit(n.children); }); } visit(tree.value);
  return Object.fromEntries(Object.entries(result).map(([k, v]) => [k, [...v]]));
});
function flash(message) { notice.value = message; clearTimeout(noticeTimer); noticeTimer = setTimeout(() => notice.value = "", 6000); }
async function loadVersions(preferred) {
  if (disposed) return false;
  const ticket = ++versionsRequestId; versionsLoading.value = true; versionsError.value = "";
  try {
    const data = await api.kpVersions(); if (ticket !== versionsRequestId) return false;
    versions.value = data.versions || []; defaultId.value = data.default_version_id || "";
    versionId.value = versions.value.some(v => v.id === preferred) ? preferred : defaultId.value || versions.value[0]?.id || "";
    return true;
  } finally { if (ticket === versionsRequestId) versionsLoading.value = false; }
}
async function reloadVersions() {
  if (busy.value) return;
  const previous = versionId.value;
  try {
    if (!await loadVersions(previous)) return;
    if (previous !== versionId.value) {
      if (modal.value?.open && modalKind.value !== "version") closeModal();
      await changeVersion();
      if (previous) flash("原大纲版本已删除，已重新载入可用版本，请重新选择操作。");
    }
  }
  catch (e) { versionsError.value = "版本加载失败：" + e.message; }
}
async function chooseVersion(id) {
  if (!versions.value.some(v => v.id === id) || id === versionId.value) return;
  versionId.value = id; await changeVersion();
}
async function removeVersion(v) {
  busy.value = true; versionsError.value = ""; let removed = false;
  try {
    await api.deleteKPVersion(v.id); removed = true;
    versions.value = versions.value.filter(item => item.id !== v.id);
    defaultId.value = versions.value.find(item => item.status === "published")?.id || "";
    if (versionId.value === v.id) { versionId.value = defaultId.value || versions.value[0]?.id || ""; await changeVersion(); }
    versionDialog.value.deleted(v.name); flash(`已删除“${v.name}”及其全部大纲要点，历史题目和任务记录保留`);
    notifyKnowledgeVersionChange();
    await loadVersions(versionId.value);
  } catch (e) {
    // A concurrent administrator may have already deleted it; refresh the stale list.
    if (e.status === 404 && !removed) {
      try { await loadVersions(versionId.value); await changeVersion(); versionDialog.value.deleted(v.name); }
      catch (refreshError) { versionsError.value = `该版本已失效，刷新列表失败：${refreshError.message}`; }
    }
    else versionsError.value = removed ? `版本已删除，刷新列表失败：${e.message}` : `删除失败：${e.message}`;
  } finally { busy.value = false; }
}
async function loadPoints() {
  if (!versionId.value) { points.value = []; total.value = 0; loading.value = false; return; }
  const ticket = ++requestId; loading.value = true; error.value = "";
  try {
    const data = await api.searchKPFiltered({ version_id: versionId.value, path: JSON.stringify(selectedPath.value), q: appliedQuery.value, page: page.value, page_size: pageSize.value });
    if (ticket !== requestId) return;
    points.value = data.points || []; total.value = data.total; page.value = data.page;
    await nextTick(); tableScroll.value?.scrollTo({ top: 0 });
  } catch (e) { if (ticket === requestId) { points.value = []; error.value = e.message; if (e.status === 404) await reloadVersions(); } }
  finally { if (ticket === requestId) loading.value = false; }
}
async function loadTree() {
  if (!versionId.value) { tree.value = []; treeLoading.value = false; return; }
  const ticket = ++treeRequestId; treeLoading.value = true;
  try {
    const data = await api.kpTree(versionId.value); if (ticket !== treeRequestId) return; tree.value = data.tree || [];
    if (selectedNode.value) {
      function find(nodes) { for (const n of nodes) { if (n.id === selectedNode.value.id) return n; const found = find(n.children); if (found) return found; } }
      selectedNode.value = find(tree.value) || null;
    }
  } catch (e) { if (ticket === treeRequestId) throw e; }
  finally { if (ticket === treeRequestId) treeLoading.value = false; }
}
async function changeVersion() {
  ++requestId; ++treeRequestId; error.value = "";
  selectedNode.value = null; expanded.value = new Set(); treeQuery.value = ""; query.value = ""; appliedQuery.value = ""; page.value = 1; tree.value = []; points.value = []; total.value = 0;
  try { await Promise.all([loadTree(), loadPoints()]); } catch (e) { error.value = e.message; }
}
async function refresh() {
  try { await loadVersions(versionId.value); await loadTree(); await loadPoints(); } catch (e) { error.value = e.message; }
}
function toggle(id) { const set = new Set(expanded.value); set.has(id) ? set.delete(id) : set.add(id); expanded.value = set; }
function select(node) { selectedNode.value = node; page.value = 1; loadPoints(); }
function search() { appliedQuery.value = query.value.trim(); page.value = 1; loadPoints(); }
function goPage(p) { page.value = p; loadPoints(); }
function expandAll() { const set = new Set(); function visit(nodes) { nodes.forEach(n => { set.add(n.id); visit(n.children); }); } visit(tree.value); expanded.value = set; }
async function openModal(kind) { formError.value = ""; modalKind.value = kind; await nextTick(); if (!modal.value.open) modal.value.showModal(); }
function closeModal() { if (!busy.value) { modal.value.close(); modalKind.value = ""; } }
function editPoint(p) {
  pointForm.value = p ? { ...p, keywordsText: (p.keywords || []).join("，") } : { category: selectedPath.value[0] || "", subject: selectedPath.value[1] || "", unit: selectedPath.value[2] || "", sub_item: selectedPath.value[3] || "", topic: "", outline_code: "", keywordsText: "", version_id: versionId.value };
  openModal("point");
}
function newVersion() { versionForm.value = { name: "", year: new Date().getFullYear(), description: "" }; openModal("version"); }
function openImport() { importFiles.value = []; importMode.value = "merge"; importResult.value = null; openModal("import"); }
function pickFiles(event) { importFiles.value = [...event.target.files]; importResult.value = null; formError.value = ""; }
async function savePoint() {
  busy.value = true; formError.value = "";
  try {
    const { keywordsText, ...body } = pointForm.value; body.keywords = keywordsText.split(/[,，;；]/).map(s => s.trim()).filter(Boolean);
    if (body.id) await api.updateKP(body.id, body); else await api.createKP(body);
    busy.value = false; closeModal(); flash(body.id ? "大纲要点已更新" : "大纲要点已新增"); await refresh();
  } catch (e) { formError.value = e.message; } finally { busy.value = false; }
}
async function saveVersion() {
  busy.value = true; formError.value = "";
  try { const v = await api.createKPVersion(versionForm.value); await loadVersions(v.id); notifyKnowledgeVersionChange(); busy.value = false; closeModal(); await changeVersion(); flash("新版本已创建，可导入整套大纲或逐条新增"); }
  catch (e) { formError.value = e.message; } finally { busy.value = false; }
}
async function publish() {
  busy.value = true; formError.value = "";
  try { await api.publishKPVersion(versionId.value); await loadVersions(versionId.value); notifyKnowledgeVersionChange(); busy.value = false; closeModal(); flash(versionId.value === defaultId.value ? "版本已启用，并设为默认大纲" : "版本已启用，可在出题时选择"); }
  catch (e) { formError.value = e.message; } finally { busy.value = false; }
}
async function runImport() {
  if (!importFiles.value.length) { formError.value = "请先选择文件"; return; }
  if (importFiles.value.length > 20 || importFiles.value.reduce((n, f) => n + f.size, 0) > 32 * 1024 * 1024) { formError.value = "一次最多 20 个文件，总大小不超过 32 MB"; return; }
  busy.value = true; formError.value = "";
  try { importResult.value = await api.importKP(importFiles.value, versionId.value, importMode.value); await refresh(); }
  catch (e) { formError.value = e.message; } finally { busy.value = false; }
}
async function exportKnowledgePoints() {
  if (!versionId.value || exporting.value) return;
  exporting.value = true; error.value = "";
  try {
    const result = await api.exportKnowledgePoints(versionId.value);
    const url = URL.createObjectURL(result.blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = result.filename || `考试大纲-${currentVersion.value?.year || "导出"}.xlsx`;
    anchor.click();
    URL.revokeObjectURL(url);
    flash(`已导出当前版本 ${currentVersion.value?.point_count || 0} 个大纲要点`);
  } catch (e) { error.value = "导出失败：" + e.message; }
  finally { exporting.value = false; }
}
function askDelete(p) { deleteTarget.value = p; openModal("delete"); }
async function removePoint() {
  busy.value = true; formError.value = "";
  try { await api.deleteKP(deleteTarget.value.id); busy.value = false; closeModal(); flash("大纲要点已删除，已有题目的快照记录保留"); await refresh(); }
  catch (e) { formError.value = e.message; } finally { busy.value = false; }
}
function showDetail(p) { detail.value = p; openModal("detail"); }
function downloadTemplate() {
  const csv = '\ufeff分类,专业/系统,单元,细目,要点,大纲代码,关键词\r\n临床综合,呼吸系统,肺部感染,肺炎,社区获得性肺炎的诊断,示例代码-请替换,肺炎\r\n';
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' })); const a = document.createElement('a'); a.href = url; a.download = '考试大纲导入模板.csv'; a.click(); URL.revokeObjectURL(url);
}
onMounted(async () => {
  try { await loadVersions(); await changeVersion(); } catch (e) { error.value = e.message; loading.value = false; }
  if (!disposed) unsubscribeVersions = subscribeKnowledgeVersions(reloadVersions);
});
onBeforeUnmount(() => { disposed = true; requestId++; treeRequestId++; versionsRequestId++; unsubscribeVersions?.(); clearTimeout(noticeTimer); });
</script>

<template>
  <section class="knowledge-workbench">
    <div class="version-bar">
      <div class="version-heading"><span class="eyebrow">当前大纲版本</span><div class="version-control"><h2>{{ currentVersion?.name || '尚未创建大纲版本' }}</h2><span v-if="currentVersion" class="state-tag" :class="{ draft: currentVersion.status === 'draft' }">{{ currentVersion.status === 'draft' ? '整理中' : versionId === defaultId ? '默认出题' : '已启用' }}</span></div></div>
      <div class="version-actions"><button class="k-btn version-switch" :disabled="busy" @click="versionDialog.open()"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 6h14m-3-3 3 3-3 3M17 14H3m3-3-3 3 3 3"/></svg>切换 / 管理版本<span>{{ versions.length }}</span></button><template v-if="canManage"><button class="k-btn" :disabled="busy" @click="newVersion">＋ 新建版本</button><button v-if="currentVersion?.status === 'draft'" class="k-btn primary" :disabled="!currentVersion.point_count || busy" @click="openModal('publish')">启用此版本</button></template></div>
    </div>
    <div class="version-caption"><span>{{ currentVersion?.description || '按考试年度维护整套大纲；不同版本的大纲要点独立管理。' }}</span><span>{{ currentVersion?.point_count || 0 }} 个大纲要点 · {{ versions.length }} 个版本</span></div>
    <div v-if="notice" class="notice" role="status">{{ notice }}<button aria-label="关闭提示" @click="notice = ''">×</button></div>
    <div v-if="error" class="error-banner" role="alert">{{ error }}<button @click="refresh">重新加载</button></div>
    <div v-if="!currentVersion && !loading && !versionsLoading" class="no-syllabus"><span>年度大纲</span><h2>从一套考试大纲开始</h2><p>新建版本并导入知识点，整理完成后启用，即可用于出题。</p><button v-if="canManage" class="k-btn primary" @click="newVersion">＋ 新建大纲版本</button><p v-else>请等待管理员创建大纲。</p></div>
    <div v-else class="catalog">
      <aside class="catalog-nav" aria-label="大纲目录">
        <div class="nav-heading"><strong>大纲目录</strong><div><button @click="expandAll" title="展开全部目录">展开</button><span>/</span><button @click="expanded = new Set()" title="收起全部目录">收起</button></div></div>
        <div class="directory-search"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m13 13 4 4"/></svg><input v-model="treeQuery" aria-label="查找目录" placeholder="查找专业、系统或单元" type="search" /></div>
        <button class="all-node" :class="{ active: !selectedNode }" @click="select(null)"><span>全部大纲要点</span><small>{{ currentVersion?.point_count || 0 }}</small></button>
        <div class="tree-scroll">
          <div v-if="treeLoading" class="tree-placeholder">正在加载目录…</div>
          <ul v-else class="tree" role="tree" aria-label="考试大纲目录树"><KnowledgeTreeNode v-for="node in filteredTree" :key="node.id" :node="node" :selected="selectedNode?.id" :expanded="visibleExpanded" @toggle="toggle" @select="select" /></ul>
          <p v-if="!treeLoading && !filteredTree.length" class="tree-placeholder">{{ treeQuery ? '没有匹配的目录' : '导入大纲要点后生成目录' }}</p>
        </div>
        <div class="nav-foot">分类 / 专业系统 / 单元 / 细目</div>
      </aside>
      <div class="catalog-content">
        <div class="content-heading"><nav class="breadcrumbs" aria-label="当前目录"><button @click="select(null)">大纲</button><template v-for="(part, i) in selectedPath" :key="i"><span>/</span><span>{{ part || '未设置目录' }}</span></template><span v-if="!selectedPath.length">/ 全部大纲要点</span></nav><div class="content-title"><div><h2>{{ contextName }}</h2><span>{{ total }} 个大纲要点{{ selectedNode ? ' · 含下级目录' : '' }}</span></div><div v-if="canManage" class="content-actions"><button class="k-btn" :disabled="!currentVersion || busy || exporting" @click="openImport"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M10 13V3m-4 4 4-4 4 4M4 12v5h12v-5"/></svg>批量导入</button><button class="k-btn" :disabled="!currentVersion || !currentVersion.point_count || busy || exporting" @click="exportKnowledgePoints"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M10 3v10m-4-4 4 4 4-4M4 17h12"/></svg>{{ exporting ? '导出中…' : '导出当前版本' }}</button><button class="k-btn primary" :disabled="!currentVersion || busy || exporting" @click="editPoint(null)">＋ 新增大纲要点</button></div></div></div>
        <form class="content-toolbar" @submit.prevent="search"><div class="search-field"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m13 13 4 4"/></svg><input v-model="query" aria-label="搜索大纲要点" placeholder="在当前目录搜索大纲要点、代码或关键词" type="search" /></div><button class="k-btn" :disabled="loading">搜索</button><button v-if="appliedQuery" class="text-btn" type="button" @click="query = ''; search()">清除搜索</button><span class="scope-hint">{{ currentVersion?.status === 'draft' ? '整理完成后启用，即可用于出题' : '当前版本内检索' }}</span></form>
        <div ref="tableScroll" class="table-scroll" :aria-busy="loading">
          <table class="knowledge-table"><thead><tr><th class="number-column">序号</th><th>大纲要点 / 所属目录</th><th class="code-column">大纲代码</th><th class="action-column">操作</th></tr></thead><tbody>
            <tr v-if="loading"><td colspan="4" class="empty-state">正在加载大纲要点…</td></tr>
            <tr v-else-if="!points.length"><td colspan="4" class="empty-state"><strong>{{ appliedQuery ? '未找到匹配的大纲要点' : '当前目录还没有大纲要点' }}</strong><p>{{ appliedQuery ? '试试其他关键词，或切换左侧目录。' : currentVersion?.status === 'draft' ? '导入整套年度大纲，或在当前目录新增大纲要点。' : '可以切换大纲版本或新增大纲要点。' }}</p><button v-if="canManage && !appliedQuery" class="k-btn" @click="openImport">导入大纲文件</button></td></tr>
            <tr v-else v-for="(p, i) in points" :key="p.id"><td class="row-number">{{ String((page - 1) * pageSize + i + 1).padStart(2, '0') }}</td><td class="topic-cell"><button class="topic-title" @click="showDetail(p)">{{ p.topic }}</button><div class="point-path">{{ [p.category, p.subject, p.unit, p.sub_item].filter(Boolean).join(' / ') }}</div><div v-if="p.keywords?.length" class="keywords"><span v-for="k in p.keywords.slice(0, 5)" :key="k">{{ k }}</span></div></td><td><code>{{ p.outline_code || '—' }}</code></td><td><div class="row-actions"><template v-if="canManage"><button @click="editPoint(p)">编辑</button><button class="delete-link" @click="askDelete(p)">删除</button></template><button v-else @click="showDetail(p)">查看</button></div></td></tr>
          </tbody></table>
        </div>
        <footer class="table-footer"><span>共 {{ total }} 条<span v-if="appliedQuery"> · 搜索“{{ appliedQuery }}”</span></span><div><select v-model.number="pageSize" aria-label="每页条数" :disabled="loading" @change="page = 1; loadPoints()"><option :value="30">30 条 / 页</option><option :value="50">50 条 / 页</option><option :value="100">100 条 / 页</option></select><button aria-label="上一页" :disabled="loading || page <= 1" @click="goPage(page - 1)">‹</button><span>{{ page }} / {{ pageCount }}</span><button aria-label="下一页" :disabled="loading || page >= pageCount" @click="goPage(page + 1)">›</button></div></footer>
      </div>
    </div>

    <KnowledgeVersionDialog ref="versionDialog" :versions="versions" :current-id="versionId" :default-id="defaultId" :can-manage="canManage" :loading="versionsLoading" :busy="busy" :error="versionsError" @refresh="reloadVersions" @select="chooseVersion" @create="newVersion" @delete="removeVersion" />
    <dialog ref="modal" class="knowledge-dialog" :class="{ wide: modalKind === 'import' }" @cancel="busy ? $event.preventDefault() : modalKind = ''">
      <div class="dialog-heading"><div><span class="eyebrow">{{ modalKind === 'version' ? '年度大纲' : currentVersion?.name }}</span><h2>{{ ({ point: pointForm.id ? '编辑大纲要点' : '新增大纲要点', version: '新建大纲版本', import: '批量导入大纲要点', delete: '删除大纲要点', publish: '启用大纲版本', detail: '大纲要点详情' })[modalKind] }}</h2></div><button class="close-dialog" aria-label="关闭弹窗" :disabled="busy" @click="closeModal">×</button></div>
      <div v-if="formError" class="form-error" role="alert">{{ formError }}</div>
      <form v-if="modalKind === 'point'" @submit.prevent="savePoint"><div class="form-grid"><label>分类 <em>*</em><input v-model="pointForm.category" list="kp-category-options" required maxlength="250" placeholder="如：临床综合" /></label><label>专业 / 系统 <em>*</em><input v-model="pointForm.subject" list="kp-subject-options" required maxlength="250" placeholder="如：呼吸系统" /></label><label>单元<input v-model="pointForm.unit" list="kp-unit-options" maxlength="250" placeholder="如：肺部感染" /></label><label>细目<input v-model="pointForm.sub_item" list="kp-sub_item-options" maxlength="250" placeholder="如：肺炎" /></label><label class="full">大纲要点内容 <em>*</em><textarea v-model="pointForm.topic" required maxlength="5000" rows="4" placeholder="填写可独立命题的具体考核要点" /></label><label class="full">大纲代码<input v-model="pointForm.outline_code" maxlength="250" placeholder="同一版本内唯一；临时补充的大纲要点可留空" /></label><label class="full">关键词<input v-model="pointForm.keywordsText" placeholder="多个关键词用逗号分隔" /></label></div><datalist v-for="(options, field) in directoryOptions" :id="`kp-${field}-options`" :key="field"><option v-for="value in options" :key="value" :value="value" /></datalist><p class="form-note">保存到当前版本。已有题目保留出题时的大纲快照。</p><div class="dialog-footer"><button type="button" class="k-btn" :disabled="busy" @click="closeModal">取消</button><button class="k-btn primary" :disabled="busy">{{ busy ? '保存中…' : '保存大纲要点' }}</button></div></form>
      <form v-if="modalKind === 'version'" @submit.prevent="saveVersion"><div class="form-grid"><label>考试年份 <em>*</em><input v-model.number="versionForm.year" type="number" min="1900" max="2200" required /></label><label>版本名称 <em>*</em><input v-model="versionForm.name" required maxlength="100" placeholder="如：2026 年临床执业医师大纲" /></label><label class="full">版本说明<textarea v-model="versionForm.description" rows="3" maxlength="2000" placeholder="适用考试、调整依据或版本备注" /></label></div><p class="form-note">新版本从空白开始，与其他年度独立。导入并启用后，系统默认展示年份最新的已启用版本。</p><div class="dialog-footer"><button class="k-btn" type="button" :disabled="busy" @click="closeModal">取消</button><button class="k-btn primary" :disabled="busy">{{ busy ? '创建中…' : '创建版本' }}</button></div></form>
      <div v-if="modalKind === 'import'">
        <div v-if="importResult" class="import-success" role="status"><strong>导入完成</strong><p>新增 {{ importResult.imported }} 条，更新 {{ importResult.updated }} 条，重复跳过 {{ importResult.duplicated }} 条。</p><p>当前版本共 {{ importResult.total }} 个大纲要点。</p></div>
        <template v-else><p class="form-note import-intro">文件中的知识点将写入 <strong>{{ currentVersion?.name }}</strong>。</p><label class="file-drop"><svg viewBox="0 0 32 32" aria-hidden="true"><path d="M19 4H8v24h16V9l-5-5Zm0 0v6h5M11 18h10m-5-5v10"/></svg><strong>选择大纲文件</strong><span>Excel .xlsx / CSV / Word .docx 表格</span><small>支持多选，最多 20 个文件，合计不超过 32 MB</small><input type="file" accept=".xlsx,.csv,.docx" multiple :disabled="busy" aria-label="选择大纲文件" @change="pickFiles" /></label><ul v-if="importFiles.length" class="file-list"><li v-for="(file, i) in importFiles" :key="i"><span>{{ file.name }}</span><small>{{ Math.ceil(file.size / 1024) }} KB</small></li></ul><div class="import-format"><div><strong>表格格式</strong><button class="text-btn" @click="downloadTemplate">下载 CSV 模板 ↓</button></div><p>分类 · 专业/系统 · 单元 · 细目 · 要点 · 大纲代码 · 关键词（可选）</p><p>支持多工作表和合并目录单元格；Word 需使用有表头的普通表格。无考核内容的“暂存”占位行会跳过。</p></div><fieldset class="import-modes"><legend>导入方式</legend><label><input type="radio" v-model="importMode" value="merge" :disabled="busy" /><span><strong>追加 / 更新</strong><small>相同代码更新内容，其他知识点保留。</small></span></label><label><input type="radio" v-model="importMode" value="replace" :disabled="busy" /><span><strong>替换此版本整套大纲</strong><small>仅保留本批文件中的知识点，其他版本及已有题目不受影响。</small></span></label></fieldset><p v-if="importMode === 'replace'" class="replace-note">当前版本的 {{ currentVersion?.point_count || 0 }} 条知识点将以本次文件为准，请确认已选择完整大纲。</p></template><div class="dialog-footer"><button class="k-btn" :disabled="busy" @click="closeModal">{{ importResult ? '完成' : '取消' }}</button><button v-if="!importResult" class="k-btn primary" :disabled="busy || !importFiles.length" @click="runImport">{{ busy ? '正在校验并导入…' : importMode === 'replace' ? '确认替换并导入' : '开始导入' }}</button></div>
      </div>
      <div v-if="modalKind === 'delete'"><p class="delete-topic">{{ deleteTarget?.topic }}</p><p class="form-note">删除后将从当前版本的目录和出题选项中移除。其他版本及已生成题目的记录保留。</p><div class="dialog-footer"><button class="k-btn" :disabled="busy" @click="closeModal">取消</button><button class="k-btn danger" :disabled="busy" @click="removePoint">{{ busy ? '删除中…' : '确认删除' }}</button></div></div>
      <div v-if="modalKind === 'publish'"><p>启用 <strong>{{ currentVersion?.name }}</strong>，共 {{ currentVersion?.point_count }} 个知识点。</p><p class="form-note">启用后可用于单题及批量出题。系统默认展示年份最新的已启用版本，历史版本仍可选择。</p><div class="dialog-footer"><button class="k-btn" :disabled="busy" @click="closeModal">取消</button><button class="k-btn primary" :disabled="busy" @click="publish">{{ busy ? '启用中…' : '确认启用' }}</button></div></div>
      <div v-if="modalKind === 'detail' && detail"><p class="detail-topic">{{ detail.topic }}</p><dl class="detail-list"><dt>所属目录</dt><dd>{{ [detail.category, detail.subject, detail.unit, detail.sub_item].filter(Boolean).join(' / ') }}</dd><dt>大纲代码</dt><dd><code>{{ detail.outline_code || '—' }}</code></dd><dt>修订号</dt><dd>{{ detail.revision }}</dd><dt>关键词</dt><dd>{{ detail.keywords?.join('、') || '—' }}</dd></dl><div class="dialog-footer"><button class="k-btn" @click="closeModal">关闭</button><button v-if="canManage" class="k-btn primary" @click="editPoint(detail)">编辑知识点</button></div></div>
    </dialog>
  </section>
</template>

<style scoped>
.knowledge-workbench { --ink: #253b38; --accent: #226c57; --border: #dce3df; --quiet: #788680; font-family: "PingFang SC", "Microsoft YaHei", sans-serif; color: var(--ink); min-width: 0; }
.version-bar { display: flex; justify-content: space-between; align-items: center; gap: 20px; padding: 0 1px 12px; }
.eyebrow { display: block; color: var(--quiet); font-size: 11px; letter-spacing: .09em; margin-bottom: 7px; }
.version-control, .version-actions, .content-actions { display: flex; align-items: center; gap: 10px; }
.version-heading { min-width: 0; }
.version-control h2 { font-size: 21px; font-weight: 600; letter-spacing: -.02em; color: var(--ink); margin: 0; line-height: 1.55; overflow-wrap: anywhere; }
.version-actions { flex-shrink: 0; }
.version-switch span { padding: 1px 5px; margin-left: 4px; background: #edf3ef; color: #7f9688; border-radius: 3px; font-size: 10px; }
.no-syllabus { text-align: center; padding: 100px 24px; background: #fff; border: 1px solid var(--border); border-radius: 7px; min-height: 430px; }
.no-syllabus > span { color: #91a396; font-size: 12px; letter-spacing: .12em; }
.no-syllabus h2 { font-size: 22px; margin: 15px 0; font-weight: 500; }
.no-syllabus p { color: #8b9c90; font-size: 13px; margin-bottom: 28px; line-height: 1.8; }
.state-tag { display: inline-block; white-space: nowrap; border: 1px solid #cee1d6; background: #edf5ef; color: #3f7657; border-radius: 4px; padding: 3px 7px; font-size: 11px; }
.state-tag.draft { background: #faf3e6; color: #987333; border-color: #eadcbd; }
.version-caption { display: flex; justify-content: space-between; gap: 20px; color: var(--quiet); font-size: 12px; margin-bottom: 20px; }
.version-caption > span:last-child { white-space: nowrap; }
.k-btn { min-height: 34px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; border: 1px solid #d6dfd9; background: #fff; padding: 7px 12px; border-radius: 5px; color: #40534b; font-size: 12px; font-weight: 500; white-space: nowrap; }
.k-btn:hover { background: #f0f4f1; border-color: #b5c6bc; }
.k-btn.primary { background: var(--accent); color: #fff; border-color: var(--accent); }
.k-btn.primary:hover { background: #17523f; }
.k-btn.danger { background: #a04136; border-color: #a04136; color: #fff; }
button:disabled { opacity: .45; cursor: not-allowed; }
button:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible { outline: 2px solid #538e73; outline-offset: 2px; }
svg { width: 17px; height: 17px; fill: none; stroke: currentColor; stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; flex-shrink: 0; }
.catalog { display: grid; grid-template-columns: 280px minmax(0, 1fr); background: #fff; border: 1px solid var(--border); border-radius: 7px; overflow: hidden; height: calc(100vh - 228px); min-height: 520px; }
.catalog-nav { display: flex; flex-direction: column; min-height: 0; background: #f7f9f7; border-right: 1px solid var(--border); padding: 19px 12px 0; }
.nav-heading { display: flex; justify-content: space-between; align-items: center; margin: 0 4px 16px; font-size: 13px; }
.nav-heading div { display: flex; gap: 5px; align-items: center; color: #b9c3bc; font-size: 11px; }
.nav-heading button { border: 0; background: transparent; color: #849087; font-size: 11px; padding: 2px; }
.directory-search, .search-field { display: flex; gap: 7px; align-items: center; border: 1px solid var(--border); border-radius: 5px; background: #fff; padding: 0 10px; color: #84928a; }
.directory-search { margin-bottom: 14px; }
.directory-search input, .search-field input { width: 100%; min-width: 0; border: 0; outline: none; padding: 9px 0; font-size: 12px; background: transparent; color: var(--ink); }
.directory-search:focus-within, .search-field:focus-within { outline: 2px solid #c3d9cc; }
.all-node { display: flex; align-items: center; justify-content: space-between; border: 0; background: transparent; padding: 11px 12px; font-size: 13px; color: #59675f; border-radius: 4px; margin-bottom: 8px; }
.all-node.active { background: #e5efeb; color: #146451; font-weight: 600; }
.all-node small { color: #81988c; font-size: 11px; }
.tree-scroll { flex: 1; min-height: 0; overflow: auto; padding-bottom: 20px; scrollbar-width: thin; scrollbar-color: #d6dfd9 transparent; }
.tree { list-style: none; margin: 0; padding: 0; }
.tree-placeholder { text-align: center; font-size: 12px; color: #87968d; padding: 25px 5px; }
.nav-foot { margin: 0 -12px; padding: 13px 14px; border-top: 1px solid #e7ece8; font-size: 10px; color: #97a399; letter-spacing: .03em; }
.catalog-content { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
.content-heading { padding: 20px 24px 18px; }
.breadcrumbs { display: flex; flex-wrap: wrap; gap: 8px; font-size: 11px; color: #8b9890; margin-bottom: 19px; }
.breadcrumbs button { border: 0; background: transparent; font-size: 11px; color: #8b9890; padding: 0; }
.content-title, .content-title > div:first-child { display: flex; align-items: center; gap: 12px; }
.content-title { justify-content: space-between; gap: 10px; }
.content-title h2 { margin: 0; font-size: 19px; font-weight: 600; color: #283e34; }
.content-title > div:first-child { flex-wrap: wrap; }
.content-title span { font-size: 11px; color: #8b9690; white-space: nowrap; }
.content-toolbar { padding: 0 24px 18px; display: flex; align-items: center; gap: 8px; }
.search-field { width: min(460px, 60%); }
.scope-hint { margin-left: auto; font-size: 11px; color: #93a097; }
.text-btn { border: 0; background: transparent; color: var(--accent); font-size: 12px; padding: 4px 0; }
.table-scroll { flex: 1; min-height: 0; overflow: auto; scrollbar-width: thin; }
.knowledge-table { width: 100%; border-collapse: collapse; table-layout: fixed; text-align: left; }
.knowledge-table th { position: sticky; top: 0; background: #f4f7f5; z-index: 1; color: #7c8980; font-weight: 500; font-size: 11px; padding: 12px 16px; border-block: 1px solid #e8ece8; }
.knowledge-table td { border-bottom: 1px solid #edf0ed; padding: 17px 16px; vertical-align: top; font-size: 12px; }
.knowledge-table tbody tr:hover { background: #fbfcfa; }
.number-column { width: 64px; }
.code-column { width: 173px; }
.action-column { width: 104px; }
.row-number { color: #99a49c; font-size: 11px !important; font-variant-numeric: tabular-nums; }
.topic-title { border: 0; padding: 0; color: #32493a; background: transparent; font-size: 13px; text-align: left; line-height: 1.65; font-weight: 500; overflow-wrap: anywhere; }
.topic-title:hover { color: var(--accent); text-decoration: underline; }
.point-path { color: #909d93; font-size: 11px; margin-top: 6px; line-height: 1.7; overflow-wrap: anywhere; }
code { font-family: "SFMono-Regular", Consolas, monospace; font-size: 11px; color: #7e8a81; word-break: break-all; line-height: 1.8; }
.row-actions { display: flex; gap: 15px; padding-top: 1px; }
.row-actions button { padding: 0; border: 0; background: transparent; font-size: 12px; color: var(--accent); white-space: nowrap; line-height: 1.8; }
.row-actions button.delete-link { color: #9c7771; }
.keywords { display: flex; gap: 5px; flex-wrap: wrap; margin-top: 7px; }
.keywords span { font-size: 10px; color: #86958b; background: #f1f5f1; padding: 2px 5px; border-radius: 3px; }
.empty-state { text-align: center; padding: 85px 30px !important; color: #8b998e; }
.empty-state strong { font-weight: 500; color: #5c7061; font-size: 15px; }
.empty-state p { font-size: 12px; margin: 12px 0 20px; }
.table-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; color: #8d9a91; font-size: 11px; border-top: 1px solid var(--border); }
.table-footer > div { display: flex; align-items: center; gap: 12px; }
.table-footer select, .table-footer button { border: 1px solid #e1e7e2; border-radius: 4px; padding: 5px 8px; background: #fff; color: #65786a; font-size: 11px; }
.table-footer button { width: 26px; font-size: 16px; padding: 1px; }
.notice, .error-banner { display: flex; justify-content: space-between; align-items: center; padding: 11px 15px; font-size: 13px; border-radius: 5px; margin-bottom: 14px; background: #e9f3ed; color: #276b4e; }
.notice button, .error-banner button { border: 0; background: transparent; color: inherit; }
.error-banner, .form-error { background: #fcf0ed; color: #a15143; }
.knowledge-dialog { width: min(580px, calc(100vw - 40px)); max-height: 90vh; overflow: auto; padding: 26px; border: 1px solid #d9e2dc; border-radius: 9px; color: var(--ink); box-shadow: 0 16px 70px #12281d26; }
.knowledge-dialog.wide { width: min(650px, calc(100vw - 40px)); }
.knowledge-dialog::backdrop { background: #142a224d; }
.dialog-heading { display: flex; justify-content: space-between; align-items: center; margin-bottom: 23px; }
.dialog-heading h2 { margin: 0; font-size: 20px; font-weight: 600; }
.close-dialog { width: 28px; height: 28px; border: 0; background: #f0f4f0; border-radius: 5px; color: #74837a; font-size: 21px; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.form-grid label { font-size: 12px; color: #65796d; }
.form-grid .full { grid-column: 1 / -1; }
.form-grid input, .form-grid textarea { display: block; margin-top: 7px; width: 100%; border: 1px solid #d7e1da; border-radius: 5px; padding: 9px 10px; color: var(--ink); background: #fff; font-size: 13px; }
.form-grid textarea { resize: vertical; line-height: 1.7; }
.form-grid em { font-style: normal; color: #ba6c5a; }
.form-note { color: #7f9085; font-size: 12px; line-height: 1.85; margin: 17px 0; }
.dialog-footer { border-top: 1px solid #e8ede9; padding-top: 19px; margin-top: 22px; display: flex; justify-content: flex-end; gap: 9px; }
.form-error { padding: 12px; border-radius: 5px; font-size: 12px; margin-bottom: 16px; line-height: 1.7; }
.file-drop { position: relative; display: flex; align-items: center; flex-direction: column; gap: 9px; padding: 27px 15px; border: 1px dashed #b5cdbb; background: #f7faf6; border-radius: 6px; cursor: pointer; }
.file-drop input { position: absolute; inset: 0; opacity: 0; width: 100%; cursor: pointer; }
.file-drop:focus-within { outline: 2px solid #538e73; }
.file-drop svg { width: 28px; height: 28px; color: #6d9478; }
.file-drop strong { font-size: 14px; font-weight: 500; }
.file-drop span { font-size: 12px; color: #789180; }
.file-drop small { font-size: 11px; color: #a0aea2; }
.file-list { padding: 0; list-style: none; max-height: 110px; overflow: auto; font-size: 12px; }
.file-list li { display: flex; justify-content: space-between; padding: 7px 0; color: #63816d; }
.import-format { margin-top: 18px; padding-bottom: 9px; border-bottom: 1px solid #e8ede9; }
.import-format > div { display: flex; justify-content: space-between; align-items: center; font-size: 12px; }
.import-format p { font-size: 11px; line-height: 1.7; color: #8a9c90; margin: 7px 0; }
.import-modes { padding: 0; border: 0; margin: 18px 0 0; }
.import-modes legend { font-size: 12px; margin-bottom: 11px; }
.import-modes label { display: flex; align-items: flex-start; gap: 8px; margin-bottom: 12px; cursor: pointer; }
.import-modes input { accent-color: #226c57; }
.import-modes strong { display: block; font-size: 12px; font-weight: 500; }
.import-modes small { display: block; font-size: 11px; color: #8b9b90; margin-top: 5px; line-height: 1.6; }
.replace-note { padding: 11px; font-size: 12px; color: #967441; background: #faf4e8; border-radius: 4px; line-height: 1.8; }
.import-success { padding: 28px; background: #f0f6ef; color: #35714b; font-size: 13px; line-height: 1.8; }
.delete-topic, .detail-topic { font-size: 16px; color: #425c49; line-height: 1.9; white-space: pre-wrap; overflow-wrap: anywhere; }
.detail-list { display: grid; grid-template-columns: 72px 1fr; gap: 14px; font-size: 13px; line-height: 1.8; }
.detail-list dt { color: #91a092; }
.detail-list dd { margin: 0; }
@media (max-width: 1180px) { .catalog { grid-template-columns: 235px minmax(0, 1fr); } .content-title { align-items: flex-start; } .content-title > div:first-child { gap: 6px; } .content-title h2 { font-size: 17px; } .content-actions { gap: 6px; } .scope-hint { display: none; } .content-heading { padding: 18px; } .content-toolbar { padding-inline: 18px; } .code-column { width: 137px; } .number-column { width: 47px; } .knowledge-table td, .knowledge-table th { padding-inline: 12px; } }
@media (max-width: 850px) { .version-bar { align-items: flex-start; } .version-control { flex-wrap: wrap; gap: 7px; } .version-control h2 { font-size: 16px; } .version-bar { flex-wrap: wrap; } .version-actions { flex-wrap: wrap; } .version-caption { line-height: 1.8; } .catalog { grid-template-columns: 200px minmax(0, 1fr); height: 680px; } .content-title { flex-direction: column; } .content-actions { margin-top: 8px; } .content-toolbar { padding-top: 0; } .search-field { width: 100%; } .knowledge-table { min-width: 450px; } .table-footer { flex-wrap: wrap; } }
@media (max-width: 600px) { .catalog { display: flex; flex-direction: column; height: auto; } .catalog-nav { max-height: 290px; border-right: 0; border-bottom: 1px solid var(--border); } .tree-scroll { min-height: 80px; } .catalog-content { min-height: 490px; } .table-scroll { max-height: 450px; } .nav-foot { display: none; } .version-caption { flex-direction: column; gap: 4px; } .version-actions { gap: 6px; } .form-grid { grid-template-columns: 1fr; } .form-grid .full { grid-column: auto; } }
</style>
