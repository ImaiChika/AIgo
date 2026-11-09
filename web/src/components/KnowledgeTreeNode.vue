<script setup>
import { computed } from "vue";
const props = defineProps({ node: Object, selected: String, expanded: Set, depth: { type: Number, default: 0 } });
const emit = defineEmits(["select", "toggle"]);
const open = computed(() => props.expanded.has(props.node.id));
function select() { emit("select", props.node); if (props.node.children.length && !open.value) emit("toggle", props.node.id); }
</script>
<template>
  <li role="none">
    <div class="tree-row" :class="{ selected: selected === node.id }" :style="{ '--depth': depth }">
      <button v-if="node.children.length" class="tree-toggle" type="button" :aria-label="`${open ? '收起' : '展开'}${node.label}`" :aria-expanded="open" @click="emit('toggle', node.id)"><svg viewBox="0 0 16 16" :class="{ open }" aria-hidden="true"><path d="m6 4 4 4-4 4" /></svg></button>
      <span v-else class="tree-leaf" aria-hidden="true">·</span>
      <button class="tree-label" type="button" role="treeitem" :aria-selected="selected === node.id" :aria-expanded="node.children.length ? open : undefined" :aria-level="depth + 1" :title="node.label" @click="select" @keydown.right.prevent="!open && node.children.length && emit('toggle', node.id)" @keydown.left.prevent="open && emit('toggle', node.id)"><span>{{ node.label }}</span><small>{{ node.count }}</small></button>
    </div>
    <ul v-if="open && node.children.length" role="group">
      <KnowledgeTreeNode v-for="child in node.children" :key="child.id" :node="child" :selected="selected" :expanded="expanded" :depth="depth + 1" @select="emit('select', $event)" @toggle="emit('toggle', $event)" />
    </ul>
  </li>
</template>
<style scoped>
ul { list-style: none; padding: 0; margin: 0; }
.tree-row { display: flex; min-height: 37px; align-items: center; padding-left: calc(7px + var(--depth) * 15px); border-radius: 4px; color: #52616b; margin: 2px 0; }
.tree-row:hover { background: #edf1f1; }
.tree-row.selected { color: #146451; background: #e5efeb; }
.tree-toggle, .tree-leaf { width: 22px; height: 30px; flex: 0 0 22px; display: grid; place-items: center; border: 0; background: transparent; padding: 2px; color: inherit; }
.tree-toggle svg { width: 14px; fill: none; stroke: currentColor; stroke-width: 1.5; transition: transform .14s; }
.tree-toggle svg.open { transform: rotate(90deg); }
.tree-label { border: 0; background: transparent; text-align: left; color: inherit; display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; font-size: 13px; padding: 8px 10px 8px 0; }
.tree-label span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tree-label small { font-size: 11px; font-variant-numeric: tabular-nums; color: #8b969a; }
.selected .tree-label { font-weight: 600; }
button:focus-visible { outline: 2px solid #267f69; outline-offset: -2px; }
</style>
