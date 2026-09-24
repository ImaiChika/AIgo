<script setup>
import { computed } from "vue";
import { pathKey } from "../knowledgePickerTree.js";

defineOptions({ name: "KnowledgePickerTreeNode" });
const props = defineProps({
  node: { type: Object, required: true },
  depth: { type: Number, default: 0 },
  expanded: { type: Set, required: true },
  selectedCounts: { type: Map, required: true },
  activeKey: { type: String, default: "" },
  busyId: { type: String, default: "" },
  showCheckbox: { type: Boolean, default: true },
});
const emit = defineEmits(["toggle", "open", "select-group"]);
const open = computed(() => props.expanded.has(props.node.id));
const selected = computed(() => props.selectedCounts.get(pathKey(props.node.path)) || 0);
const checked = computed(() => props.node.count > 0 && selected.value >= props.node.count);
const partial = computed(() => selected.value > 0 && !checked.value);

function openDirectory() {
  emit("open", props.node);
  if (props.node.children.length && !open.value) emit("toggle", props.node.id);
}
</script>

<template>
  <li class="directory-node">
    <div class="directory-row" :class="{ active: activeKey === pathKey(node.path), chosen: checked, 'level-one': depth === 0, 'level-two': depth === 1 }" :style="{ '--depth': depth }">
      <button v-if="node.children.length" class="directory-expand" type="button" :aria-label="`${open ? '收起' : '展开'}${node.label}`" :aria-expanded="open" @click="emit('toggle', node.id)">{{ open ? '−' : '+' }}</button>
      <span v-else class="directory-expand leaf" aria-hidden="true">·</span>
      <input v-if="showCheckbox" type="checkbox" class="directory-check" :checked="checked" :indeterminate="partial" :disabled="!!busyId" :aria-label="`选择${node.label}下全部${node.count}个大纲要点`" @change="emit('select-group', node)" />
      <button class="directory-label" type="button" :title="node.label" @click="openDirectory"><span>{{ node.label }}</span><small>{{ selected ? `${selected}/` : '' }}{{ node.count }}</small></button>
    </div>
    <ul v-if="open && node.children.length" class="directory-children">
      <KnowledgePickerTreeNode v-for="child in node.children" :key="child.id" :node="child" :depth="depth + 1" :expanded="expanded" :selected-counts="selectedCounts" :active-key="activeKey" :busy-id="busyId" :show-checkbox="showCheckbox" @toggle="emit('toggle', $event)" @open="emit('open', $event)" @select-group="emit('select-group', $event)" />
    </ul>
  </li>
</template>

<style scoped>
.directory-node, .directory-children { list-style: none; margin: 0; padding: 0; }
.directory-row { display: flex; align-items: center; min-height: 38px; padding-left: calc(4px + var(--depth) * 15px); border-radius: 5px; color: #52616b; }
.directory-row:hover { background: #f0f5fb; }
.directory-row.active { color: #075eac; background: #e9f3ff; }
.directory-row.chosen .directory-label { font-weight: 600; }
.directory-row.level-one { color: #183b61; background: #f2f7fd; margin: 4px 0; }
.directory-row.level-one .directory-label { font-weight: 750; font-size: 14px; }
.directory-row.level-two { color: #315a7e; }
.directory-row.level-two .directory-label { font-weight: 700; }
.directory-row.level-one.active, .directory-row.level-two.active { color: #075eac; background: #e9f3ff; }
.directory-expand { border: 0; background: transparent; width: 24px; flex: 0 0 24px; height: 30px; color: #8998a8; font-size: 19px; cursor: pointer; line-height: 1; }
.directory-expand.leaf { display: grid; place-items: center; font-size: 16px; cursor: default; }
.directory-check { width: 16px; height: 16px; flex: 0 0 16px; margin: 0 8px 0 0; accent-color: #1684ed; cursor: pointer; }
.directory-label { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; padding: 7px 8px 7px 0; border: 0; background: transparent; color: inherit; text-align: left; cursor: pointer; font-size: 13px; }
.directory-label span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.directory-label small { color: #8796a8; font-size: 11px; font-variant-numeric: tabular-nums; }
button:focus-visible, input:focus-visible { outline: 2px solid #1684ed; outline-offset: 2px; }
</style>
