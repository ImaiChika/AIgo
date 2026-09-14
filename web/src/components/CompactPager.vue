<script setup>
import { computed } from "vue";

const props = defineProps({
  page: { type: Number, required: true },
  totalPages: { type: Number, required: true },
  disabled: { type: Boolean, default: false },
});
const emit = defineEmits(["change"]);

const pages = computed(() => {
  const total = Math.max(1, props.totalPages);
  const start = Math.max(1, Math.min(props.page - 2, total - 4));
  const end = Math.min(total, start + 4);
  const result = [];
  for (let page = start; page <= end; page++) result.push(page);
  return result;
});

function go(page) {
  if (props.disabled || page < 1 || page > props.totalPages || page === props.page) return;
  emit("change", page);
}
</script>

<template>
  <nav class="compact-pager" aria-label="任务分页">
    <button type="button" :disabled="disabled || page <= 1" aria-label="上一页" @click="go(page - 1)">‹</button>
    <button
      v-for="item in pages"
      :key="item"
      type="button"
      :class="{ active: item === page }"
      :aria-current="item === page ? 'page' : undefined"
      :disabled="disabled"
      @click="go(item)"
    >{{ item }}</button>
    <button type="button" :disabled="disabled || page >= totalPages" aria-label="下一页" @click="go(page + 1)">›</button>
  </nav>
</template>

<style scoped>
.compact-pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding-top: 10px;
  border-top: 1px solid #edf1f5;
}

.compact-pager button {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  padding: 0;
  border: 1px solid #dfe6ee;
  border-radius: 5px;
  color: #607086;
  background: #fff;
  font-size: 12px;
}

.compact-pager button:hover:not(:disabled) {
  border-color: #7fb5e4;
  color: #086fc9;
}

.compact-pager button.active {
  border-color: #1385f8;
  color: #fff;
  background: #1385f8;
}

.compact-pager button:disabled {
  opacity: .45;
  cursor: not-allowed;
}
</style>
