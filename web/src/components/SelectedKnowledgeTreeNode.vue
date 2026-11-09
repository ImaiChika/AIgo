<script setup>
import { computed, ref } from "vue";

defineOptions({ name: "SelectedKnowledgeTreeNode" });
const props = defineProps({ node: { type: Object, required: true }, depth: { type: Number, default: 0 } });
const emit = defineEmits(["remove", "remove-group"]);
const open = ref(false);
const childPage = ref(1);
const pointPage = ref(1);
const childPageSize = 20;
const pointPageSize = 30;
const visibleChildren = computed(() => props.node.children.slice((childPage.value - 1) * childPageSize, childPage.value * childPageSize));
const visiblePoints = computed(() => props.node.points.slice((pointPage.value - 1) * pointPageSize, pointPage.value * pointPageSize));
const childPages = computed(() => Math.max(1, Math.ceil(props.node.children.length / childPageSize)));
const pointPages = computed(() => Math.max(1, Math.ceil(props.node.points.length / pointPageSize)));
</script>

<template>
  <li class="selected-node">
    <div class="selected-row" :style="{ '--depth': depth }">
      <button class="selected-expand" type="button" :aria-label="`${open ? '收起' : '展开'}已选${node.label}`" :aria-expanded="open" @click="open = !open"><span>{{ open ? '−' : '+' }}</span></button>
      <button class="selected-label" type="button" :title="node.label" @click="open = !open">{{ node.label }}</button>
      <span class="selected-count">{{ node.count }} 个</span>
      <button class="remove-group" type="button" :aria-label="`移除${node.label}下已选的${node.count}个要点`" @click="emit('remove-group', node.path)">移除本组</button>
    </div>
    <div v-if="open" class="selected-branch">
      <ul v-if="node.children.length" class="selected-children">
        <SelectedKnowledgeTreeNode v-for="child in visibleChildren" :key="child.key" :node="child" :depth="depth + 1" @remove="emit('remove', $event)" @remove-group="emit('remove-group', $event)" />
      </ul>
      <div v-if="childPages > 1" class="branch-pager"><button type="button" :disabled="childPage <= 1" @click="childPage--">上一页</button><span>子目录 {{ childPage }} / {{ childPages }}</span><button type="button" :disabled="childPage >= childPages" @click="childPage++">下一页</button></div>
      <div v-if="node.points.length" class="selected-points">
        <div v-for="point in visiblePoints" :key="point.id" class="selected-point"><span class="point-topic" :title="point.topic">{{ point.topic }}</span><code v-if="point.outline_code">{{ point.outline_code }}</code><button type="button" :aria-label="`移除${point.topic}`" @click="emit('remove', point.id)">移除</button></div>
      </div>
      <div v-if="pointPages > 1" class="branch-pager"><button type="button" :disabled="pointPage <= 1" @click="pointPage--">上一页</button><span>考点 {{ pointPage }} / {{ pointPages }}</span><button type="button" :disabled="pointPage >= pointPages" @click="pointPage++">下一页</button></div>
    </div>
  </li>
</template>

<style scoped>
.selected-node, .selected-children { list-style: none; margin: 0; padding: 0; }
.selected-row { min-height: 42px; display: flex; align-items: center; gap: 8px; padding: 3px 8px 3px calc(7px + var(--depth) * 17px); border-bottom: 1px solid #e6eef7; }
.selected-row:hover { background: #f4f8ff; }
.selected-expand { width: 22px; height: 22px; flex: 0 0 22px; border: 1px solid #cfe0f2; background: #fff; border-radius: 4px; color: #3b7cbb; cursor: pointer; }
.selected-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; border: 0; background: transparent; color: #263a52; font-size: 13px; font-weight: 600; cursor: pointer; }
.selected-count { color: #487da8; font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.remove-group { border: 0; background: transparent; color: #b95c64; font-size: 11px; cursor: pointer; white-space: nowrap; }
.selected-branch { margin-left: 16px; border-left: 1px dashed #cad9e9; }
.selected-points { padding: 2px 4px 4px 22px; }
.selected-point { display: flex; gap: 8px; align-items: baseline; padding: 6px 7px; border-bottom: 1px solid #edf3f9; font-size: 12px; color: #42546b; }
.point-topic { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.selected-point code { color: #1478cc; font-size: 11px; white-space: nowrap; }
.selected-point button { border: 0; background: transparent; color: #b95c64; cursor: pointer; }
.branch-pager { display: flex; gap: 8px; align-items: center; justify-content: flex-end; padding: 7px 10px; color: #6d7e91; font-size: 11px; }
.branch-pager button { border: 1px solid #d8e5f2; border-radius: 4px; background: #fff; color: #2d6fa9; padding: 3px 7px; cursor: pointer; }
.branch-pager button:disabled { opacity: .45; cursor: default; }
button:focus-visible { outline: 2px solid #1684ed; outline-offset: 2px; }
</style>
