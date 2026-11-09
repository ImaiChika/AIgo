<script setup>
// 结构化评语输入：题干 / 选项 / 答案与解析 / 其他 四栏。
// 规则提示由父级展示，本组件只负责收集内容。
const props = defineProps({
  comment: { type: Object, required: true }, // { stem, options, answer, other }
  disabled: { type: Boolean, default: false },
});

const emit = defineEmits(["update:comment"]);

const sections = [
  { key: "stem", label: "题干意见", placeholder: "题干表述、临床情景、信息完整性…" },
  { key: "options", label: "选项意见", placeholder: "选项设置、干扰项、表述歧义…" },
  { key: "answer", label: "答案与解析", placeholder: "正确答案、解析依据…" },
  { key: "other", label: "其他意见", placeholder: "难度、考点、格式等…" },
];

function update(key, value) {
  emit("update:comment", { ...props.comment, [key]: value });
}
</script>

<template>
  <div class="sc-input" :class="{ disabled }">
    <div v-for="s in sections" :key="s.key" class="sc-field">
      <label>{{ s.label }}</label>
      <textarea
        :value="comment[s.key] || ''"
        :placeholder="s.placeholder"
        :disabled="disabled"
        rows="2"
        @input="update(s.key, $event.target.value)"
      ></textarea>
    </div>
  </div>
</template>

<style scoped>
.sc-input {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}

.sc-field label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 3px;
}

.sc-field textarea {
  width: 100%;
  box-sizing: border-box;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 6px 8px;
  font-size: 12px;
  line-height: 1.55;
  color: #172033;
  resize: vertical;
  min-height: 52px;
  font-family: inherit;
}

.sc-field textarea:focus {
  outline: none;
  border-color: #1385f8;
}

.sc-field textarea::placeholder {
  color: #b3bdcb;
}

.sc-input.disabled {
  opacity: 0.6;
}
</style>
