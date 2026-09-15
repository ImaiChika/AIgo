<script setup>
import { ref, computed, watch, nextTick, useId } from "vue";
const props = defineProps({
  versions: { type: Array, default: () => [] }, currentId: String, defaultId: String,
  canManage: Boolean, loading: Boolean, busy: Boolean, error: String,
});
const emit = defineEmits(["refresh", "select", "create", "delete"]);
const dialog = ref(null), query = ref(""), filter = ref("all"), selectedId = ref(""), deleteTarget = ref(null), notice = ref("");
const titleId = useId();
const selectedVersion = computed(() => props.versions.find(v => v.id === selectedId.value));
const filteredVersions = computed(() => {
  const term = query.value.trim().toLowerCase();
  return props.versions.filter(v => (filter.value === "all" || v.status === filter.value) && (!term || `${v.name} ${v.year || ''} ${v.description}`.toLowerCase().includes(term)));
});
const fallback = computed(() => props.versions.find(v => v.id !== deleteTarget.value?.id && v.status === "published"));
function date(value) { return value ? new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date(value)) : "—"; }
async function open() {
  query.value = ""; filter.value = "all"; selectedId.value = props.currentId || ""; deleteTarget.value = null; notice.value = "";
  await nextTick(); dialog.value.showModal(); emit("refresh");
}
function close() { if (!props.busy) dialog.value.close(); }
function choose() { if (!selectedVersion.value || props.busy || props.loading) return; const id = selectedId.value; close(); emit("select", id); }
function create() { close(); emit("create"); }
function confirmDelete(v) { deleteTarget.value = v; notice.value = ""; }
function deleted(name) { deleteTarget.value = null; selectedId.value = props.currentId || ""; notice.value = `已删除“${name}”`; }
watch(() => props.versions, list => { if (!list.some(v => v.id === selectedId.value)) selectedId.value = props.currentId || ""; });
defineExpose({ open, deleted });
</script>

<template>
  <dialog ref="dialog" class="version-dialog" :aria-labelledby="titleId" @cancel="busy && $event.preventDefault()">
    <header class="version-dialog-header">
      <div><span class="dialog-kicker">考试大纲 / 版本</span><h2 :id="titleId">{{ deleteTarget ? '删除整套大纲版本' : '选择大纲版本' }}</h2><p v-if="!deleteTarget">按考试年度选择独立大纲，当前工作内容随版本切换。</p></div>
      <button class="icon-button" type="button" aria-label="关闭版本窗口" :disabled="busy" @click="close">×</button>
    </header>
    <div v-if="error" class="dialog-error" role="alert">{{ error }}<button v-if="!deleteTarget" type="button" @click="emit('refresh')">重试</button></div>
    <template v-if="deleteTarget">
      <div class="delete-body">
        <div class="delete-summary"><span class="year-box">{{ deleteTarget.year || '历史' }}</span><div><h3>{{ deleteTarget.name }}</h3><p>{{ deleteTarget.point_count }} 个大纲要点 · {{ deleteTarget.status === 'draft' ? '整理中' : '已启用' }}</p></div></div>
        <p class="delete-explanation">此版本及其全部大纲要点将从管理列表和出题选项中移除，无法再用于新出题。</p>
        <div class="retained-note">已有题目、审核记录及已提交的批量任务保留原有内容和关联。</div>
        <p v-if="deleteTarget.id === defaultId" class="fallback-note">{{ fallback ? `删除后，默认出题大纲将切换为“${fallback.name}”。` : '删除后将没有已启用的大纲，启用其他版本后可继续出题。' }}</p>
        <p v-else-if="versions.length === 1" class="fallback-note">这是最后一个版本。删除后可从空白重新创建大纲。</p>
      </div>
      <footer class="version-dialog-footer"><button class="version-btn" type="button" :disabled="busy" @click="deleteTarget = null">返回版本列表</button><button class="version-btn danger" type="button" :disabled="busy || loading" @click="emit('delete', deleteTarget)">{{ busy ? '正在删除…' : '确认删除整套版本' }}</button></footer>
    </template>
    <template v-else>
      <div class="version-filter-bar"><div class="version-search"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m13 13 4 4"/></svg><input v-model="query" type="search" aria-label="搜索大纲版本" autofocus placeholder="搜索年份、版本名称或说明" /></div><button v-if="canManage" class="version-btn" type="button" :disabled="busy" @click="create">＋ 新建版本</button></div>
      <div class="version-filters" aria-label="版本状态筛选"><button v-for="item in [{value:'all',label:'全部版本'},{value:'published',label:'已启用'},{value:'draft',label:'整理中'}].filter(i => canManage || i.value !== 'draft')" :key="item.value" type="button" :aria-pressed="filter === item.value" :class="{ active: filter === item.value }" @click="filter = item.value">{{ item.label }}<span>{{ versions.filter(v => item.value === 'all' || v.status === item.value).length }}</span></button></div>
      <div v-if="notice" class="version-notice" role="status">{{ notice }}</div>
      <div class="version-list" :aria-busy="loading">
        <p v-if="loading" class="empty-versions">正在加载版本…</p>
        <div v-else-if="!filteredVersions.length" class="empty-versions"><strong>{{ versions.length ? '没有匹配的版本' : '暂无可选大纲版本' }}</strong><p>{{ versions.length ? '调整关键词或筛选条件再试。' : canManage ? '新建版本后，导入整套年度考试大纲。' : '请等待管理员导入并启用大纲。' }}</p></div>
        <article v-else v-for="v in filteredVersions" :key="v.id" class="version-item" :class="{ chosen: selectedId === v.id }">
          <label class="version-choice"><input type="radio" :name="`${titleId}-version`" :value="v.id" v-model="selectedId" :aria-label="v.name" /><span class="year-box">{{ v.year || '历史' }}</span><span class="version-info"><span class="version-name">{{ v.name }}<span v-if="v.id === currentId" class="version-badge current">当前</span><span v-if="v.id === defaultId" class="version-badge default">默认出题</span><span v-else class="version-badge" :class="{ draft: v.status === 'draft' }">{{ v.status === 'draft' ? '整理中' : '已启用' }}</span></span><span v-if="v.description" class="version-description">{{ v.description }}</span><span class="version-meta"><span>{{ v.point_count.toLocaleString() }} 个大纲要点</span><span>{{ v.status === 'published' ? '启用' : '创建' }}于 {{ date(v.published_at || v.created_at) }}</span></span></span></label>
          <button v-if="canManage" class="delete-version" type="button" :aria-label="`删除版本：${v.name}`" :disabled="busy" @click="confirmDelete(v)">删除</button>
        </article>
      </div>
      <footer class="version-dialog-footer"><p>{{ selectedVersion ? `已选择：${selectedVersion.name}` : '请选择一个大纲版本' }}</p><div><button class="version-btn" type="button" @click="close">取消</button><button class="version-btn primary" type="button" :disabled="!selectedVersion || busy || loading" @click="choose">{{ selectedId === currentId ? '返回当前版本' : '切换到此版本' }}</button></div></footer>
    </template>
  </dialog>
</template>

<style scoped>
.version-dialog { --accent: #226c57; width: min(800px, calc(100vw - 32px)); padding: 0; border: 1px solid #d9e2dc; border-radius: 10px; color: #2c4138; font-family: "PingFang SC", "Microsoft YaHei", sans-serif; box-shadow: 0 24px 100px #102e2833; max-height: 88vh; overflow-y: auto; }
.version-dialog::backdrop { background: #142a225e; }
.version-dialog-header { display: flex; justify-content: space-between; align-items: flex-start; padding: 25px 28px 22px; border-bottom: 1px solid #e4ebe6; }
.dialog-kicker { font-size: 10px; letter-spacing: .12em; color: #89998f; }
h2 { font-size: 22px; margin: 9px 0 8px; font-weight: 600; letter-spacing: -.03em; }
.version-dialog-header p { font-size: 12px; margin: 0; color: #88958d; line-height: 1.8; }
.icon-button { background: #f4f6f4; border: 0; border-radius: 5px; height: 29px; width: 29px; font-size: 22px; color: #86938c; }
.version-filter-bar { display: flex; gap: 14px; padding: 20px 28px 16px; }
.version-search { display: flex; align-items: center; gap: 9px; border: 1px solid #dbe3dc; border-radius: 5px; padding: 0 11px; flex: 1; min-width: 0; }
.version-search svg { width: 16px; height: 16px; fill: none; stroke: #8a9a8f; stroke-width: 1.4; flex-shrink: 0; }
.version-search input { border: 0; background: transparent; width: 100%; min-width: 0; padding: 10px 0; outline: none; font-size: 12px; color: #3b5344; }
.version-search:focus-within { outline: 2px solid #c1d9cb; }
.version-filters { display: flex; gap: 18px; padding: 0 28px; border-bottom: 1px solid #e9eee9; }
.version-filters button { display: flex; gap: 7px; align-items: center; background: transparent; border: 0; border-bottom: 2px solid transparent; padding: 0 0 11px; color: #8d9b91; font-size: 12px; }
.version-filters button.active { border-color: var(--accent); color: var(--accent); font-weight: 600; }
.version-filters span { border-radius: 4px; background: #f0f4f1; padding: 1px 5px; font-size: 10px; }
.version-list { min-height: 200px; max-height: min(430px, 44vh); overflow: auto; padding: 18px 28px; background: #fbfcfa; scrollbar-width: thin; }
.version-item { display: flex; align-items: center; gap: 12px; border: 1px solid #e2e8e1; border-radius: 6px; background: #fff; margin-bottom: 10px; padding: 18px 17px; }
.version-item:last-child { margin-bottom: 0; }
.version-item:hover { border-color: #bcd0bf; }
.version-item.chosen { border-color: #83ac92; background: #f3f8f3; box-shadow: 0 0 0 1px #83ac921a; }
.version-choice { display: flex; align-items: center; flex: 1; gap: 14px; min-width: 0; cursor: pointer; }
.version-choice input { margin: 0; accent-color: var(--accent); }
.year-box { display: grid; place-items: center; width: 53px; min-height: 53px; flex-shrink: 0; border: 1px solid #e2e9df; border-radius: 5px; background: #f8faf5; color: #5d7964; font-size: 17px; font-weight: 600; font-variant-numeric: tabular-nums; }
.version-info { flex: 1; min-width: 0; }
.version-name { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; font-size: 14px; font-weight: 600; line-height: 1.7; overflow-wrap: anywhere; }
.version-badge { padding: 1px 5px; border-radius: 3px; background: #eef2ee; color: #819184; font-size: 10px; white-space: nowrap; font-weight: 400; }
.version-badge.current { color: #4c6b5a; background: #e2ece4; }
.version-badge.default { color: #2e7860; background: #e4f0e8; }
.version-badge.draft { color: #9c7c4c; background: #f6efe2; }
.version-description { display: block; margin: 4px 0 7px; color: #8c9a90; font-size: 11px; line-height: 1.7; overflow-wrap: anywhere; }
.version-meta { display: flex; flex-wrap: wrap; gap: 18px; color: #98a49b; font-size: 10px; margin-top: 5px; }
.delete-version { background: transparent; border: 0; font-size: 11px; color: #a68075; padding: 8px 0 8px 10px; align-self: flex-start; }
.delete-version:hover { color: #ae493b; }
.version-dialog-footer { border-top: 1px solid #e5ebe4; padding: 17px 28px; display: flex; align-items: center; justify-content: space-between; gap: 20px; background: #fff; }
.version-dialog-footer p { font-size: 11px; color: #8b9a90; margin: 0; overflow-wrap: anywhere; }
.version-dialog-footer > div { display: flex; gap: 8px; flex-shrink: 0; }
.version-btn { display: inline-flex; align-items: center; justify-content: center; border: 1px solid #d9e2db; background: white; color: #5b7263; border-radius: 5px; min-height: 35px; padding: 8px 13px; font-size: 12px; white-space: nowrap; }
.version-btn:hover { background: #f1f6f2; }
.version-btn.primary { background: var(--accent); border-color: var(--accent); color: white; }
.version-btn.danger { background: #a74c3d; color: #fff; border-color: #a74c3d; }
button:disabled { opacity: .45; cursor: not-allowed; }
button:focus-visible, input:focus-visible { outline: 2px solid #548a6d; outline-offset: 2px; }
.empty-versions { padding: 42px 15px; text-align: center; font-size: 12px; color: #8c9b8d; line-height: 1.9; }
.empty-versions strong { font-size: 14px; font-weight: 500; color: #607a63; }
.version-notice, .dialog-error { margin: 14px 28px 0; padding: 11px 13px; background: #ecf5ed; color: #4f7958; border-radius: 5px; font-size: 12px; }
.dialog-error { background: #faf0ed; color: #a46553; display: flex; justify-content: space-between; }
.dialog-error button { border: 0; background: none; color: inherit; }
.delete-body { padding: 26px 28px 14px; }
.delete-summary { display: flex; gap: 16px; align-items: center; padding: 20px; background: #f8f9f5; border: 1px solid #e5e9df; border-radius: 7px; }
.delete-summary h3 { margin: 0 0 7px; font-size: 16px; font-weight: 600; overflow-wrap: anywhere; }
.delete-summary p { margin: 0; font-size: 12px; color: #8f998c; }
.delete-explanation { font-size: 14px; line-height: 1.85; margin-top: 23px; }
.retained-note { font-size: 12px; color: #81927f; line-height: 1.8; }
.fallback-note { font-size: 12px; color: #967849; background: #faf5e9; padding: 12px 14px; border-radius: 5px; line-height: 1.8; margin-top: 20px; }
@media(max-width: 600px) { .version-dialog-header,.version-filter-bar,.version-list,.version-dialog-footer,.delete-body { padding-left: 18px; padding-right: 18px; } .version-filters { padding-inline: 18px; } .version-choice { gap: 9px; } .year-box { width: 42px; min-height: 44px; font-size: 14px; } .version-item { padding: 13px 10px; gap: 4px; } .version-name { font-size: 12px; } .version-dialog-footer { flex-wrap: wrap; gap: 12px; } .version-dialog-footer > div { margin-left: auto; } .version-meta { gap: 6px; } }
</style>
