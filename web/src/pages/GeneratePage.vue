<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";

const topics = ref([]);
const selectedTopic = ref("");
const selectedCount = ref(1);
const selectedImage = ref(-1);
const toast = ref("");
const stem = ref("");
const options = ref([]);
const answer = ref("");
const explanation = ref("");
const promptText = ref("");
const loading = ref(false);
const imageLoading = ref(false);
const generatedImages = ref([]);
const currentQuestionId = ref("");
const stats = ref({ question_count: 0, knowledge_count: 0 });
const progressMsg = ref("");
const startTime = ref(0);
const generatedQuestions = ref([]);
const currentIndex = ref(0);

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadStats() {
  try {
    stats.value = await api.stats();
  } catch (e) {
    console.error(e);
  }
}

async function loadTopics() {
  try {
    const data = await api.listKP({ page: 1, page_size: 50 });
    topics.value = data.points || [];
    if (topics.value.length > 0) {
      selectedTopic.value = topics.value[0].topic;
    }
  } catch (e) {
    showToast("加载知识点失败: " + e.message);
  }
}

async function generateQuestion() {
  if (!selectedTopic.value) {
    showToast("请先选择知识点");
    return;
  }
  loading.value = true;
  progressMsg.value = "正在调用千问生成试题，请稍候...";
  startTime.value = Date.now();

  // 进度计时器
  const timer = setInterval(() => {
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(0);
    progressMsg.value = `千问生成中... 已等待 ${elapsed} 秒`;
  }, 1000);

  try {
    const data = await api.generate({
      subject: "临床医学",
      difficulty: "medium",
      topic: selectedTopic.value,
      count: selectedCount.value,
    });
    clearInterval(timer);
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(1);
    if (data.questions && data.questions.length > 0) {
      generatedQuestions.value = data.questions;
      currentIndex.value = 0;
      showQuestion(0);
      progressMsg.value = `✓ 已生成 ${data.count} 道题，耗时 ${elapsed} 秒`;
      showToast(`已生成 ${data.count} 道题，耗时 ${elapsed} 秒`);
      loadStats();
    }
  } catch (e) {
    clearInterval(timer);
    progressMsg.value = `✗ 生成失败: ${e.message}`;
    showToast("生成失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function showQuestion(index) {
  if (index < 0 || index >= generatedQuestions.value.length) return;
  currentIndex.value = index;
  const q = generatedQuestions.value[index];
  stem.value = q.clinical_stem || "";
  options.value = (q.options || []).map((o) => o.text);
  answer.value = q.answer || "";
  explanation.value = q.explanation || "";
  currentQuestionId.value = q.id || "";
  generatedImages.value = [];
  selectedImage.value = -1;
  promptText.value = "";
}

function prevQuestion() {
  if (currentIndex.value > 0) showQuestion(currentIndex.value - 1);
}

function nextQuestion() {
  if (currentIndex.value < generatedQuestions.value.length - 1) showQuestion(currentIndex.value + 1);
}

function removeOption(index) {
  if (options.value.length <= 4) {
    showToast("A2 单选题至少保留 4 个选项");
    return;
  }
  options.value.splice(index, 1);
}

async function saveQuestion() {
  if (!currentQuestionId.value) {
    showToast("没有可保存的题目");
    return;
  }
  try {
    const data = await api.updateQuestion(currentQuestionId.value, {
      clinical_stem: stem.value,
      options: options.value.map((text, i) => ({ label: String.fromCharCode(65 + i), text })),
      answer: answer.value,
      explanation: explanation.value,
    });
    // 同步更新本地数据
    if (generatedQuestions.value[currentIndex.value]) {
      generatedQuestions.value[currentIndex.value] = data;
    }
    showToast("已保存到题库");
  } catch (e) {
    showToast("保存失败: " + e.message);
  }
}

async function generatePrompt() {
  if (!currentQuestionId.value) {
    showToast("请先生成题目");
    return;
  }
  try {
    const data = await api.imagePrompt(currentQuestionId.value);
    const lines = [
      `图片用途：${data.purpose}`,
      `图片类型：${data.image_type}`,
      `医学主题：${data.subject}`,
      `必须出现：${(data.must_include || []).join("、")}`,
      `不能出现：${(data.must_exclude || []).join("、")}`,
      `风格：${data.style}`,
      `审核重点：${data.review_focus}`,
    ];
    promptText.value = lines.join("\n");
    showToast("已生成结构化提示词");
  } catch (e) {
    showToast("生成提示词失败: " + e.message);
  }
}

async function generateImages() {
  if (!currentQuestionId.value) {
    showToast("请先生成题目");
    return;
  }
  imageLoading.value = true;
  try {
    const data = await api.imageGenerate(currentQuestionId.value, 4);
    generatedImages.value = data.images || [];
    selectedImage.value = 0;
    showToast(`已生成 ${data.count} 张候选图`);
  } catch (e) {
    showToast("生成图片失败: " + e.message);
  } finally {
    imageLoading.value = false;
  }
}

function selectImage(index) {
  selectedImage.value = index;
  showToast(`已选择候选图 ${index + 1}`);
}

function addOption() {
  if (options.value.length >= 5) {
    showToast("A2 单选题最多保留 A-E 五个选项");
    return;
  }
  options.value.push("新增选项");
}

function optionLabel(index) {
  return String.fromCharCode(65 + index);
}

onMounted(() => {
  loadStats();
  loadTopics();
});
</script>

<template>
  <div class="main-grid">
    <div class="editor-column">
      <!-- 多题切换 -->
      <section v-if="generatedQuestions.length > 1" class="panel question-nav">
        <button class="ghost-button" type="button" :disabled="currentIndex <= 0" @click="prevQuestion">← 上一题</button>
        <span class="nav-info">第 {{ currentIndex + 1 }} / {{ generatedQuestions.length }} 题</span>
        <button class="ghost-button" type="button" :disabled="currentIndex >= generatedQuestions.length - 1" @click="nextQuestion">下一题 →</button>
      </section>

      <!-- 题干 -->
      <section class="panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>题干编辑</h2>
        </div>
        <textarea v-model="stem" class="stem-input" placeholder="点击右侧「AI自动生成试题」或手动编辑题干"></textarea>
      </section>

      <!-- 选项 -->
      <section class="panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>选项编辑</h2>
          <small v-if="answer">正确答案：{{ answer }}</small>
        </div>
        <div class="options-list">
          <div v-for="(_, index) in options" :key="index" class="option-row">
            <span class="option-label">{{ optionLabel(index) }}</span>
            <input v-model="options[index]" />
            <button class="remove-btn" type="button" @click="removeOption(index)" title="删除选项">×</button>
          </div>
        </div>
        <button class="text-button" type="button" @click="addOption">+ 添加选项</button>

        <div class="answer-row">
          <label>正确答案：</label>
          <select v-model="answer">
            <option v-for="(_, i) in options" :key="i" :value="String.fromCharCode(65 + i)">
              {{ String.fromCharCode(65 + i) }}
            </option>
          </select>
        </div>
      </section>

      <!-- 解析 -->
      <section v-if="explanation" class="panel">
        <div class="section-heading">
          <span class="dot teal"></span>
          <h2>答案解析</h2>
        </div>
        <textarea v-model="explanation" class="explanation-input"></textarea>
      </section>

      <!-- 保存按钮 -->
      <section v-if="currentQuestionId" class="panel save-section">
        <button class="primary-button" type="button" @click="saveQuestion">
          保存修改到题库
        </button>
        <span class="save-hint">修改题干、选项、答案或解析后点击保存</span>
      </section>

      <!-- AI配图 -->
      <section class="panel">
        <div class="section-heading">
          <span class="dot teal"></span>
          <h2>AI配图候选</h2>
          <small>选择一张后提交专家审核</small>
        </div>
        <div class="image-prompt">
          <label>结构化生图提示词</label>
          <textarea v-model="promptText" placeholder="点击「生成提示词」自动生成"></textarea>
          <div class="button-row">
            <button class="outline-button" type="button" @click="generatePrompt" :disabled="!currentQuestionId">
              生成提示词
            </button>
            <button class="outline-button" type="button" @click="generateImages" :disabled="!currentQuestionId || imageLoading">
              {{ imageLoading ? "生成中..." : "生成4张候选图" }}
            </button>
          </div>
        </div>
        <div v-if="generatedImages.length" class="candidate-grid">
          <button
            v-for="(img, index) in generatedImages"
            :key="img.id"
            class="candidate-card"
            :class="{ selected: selectedImage === index }"
            type="button"
            @click="selectImage(index)"
          >
            <div class="candidate-placeholder">
              <span>{{ index + 1 }}</span>
            </div>
            <div class="candidate-meta">
              <span>{{ img.model_name }}</span>
              <span class="status">{{ selectedImage === index ? "已选择" : img.status }}</span>
            </div>
          </button>
        </div>
      </section>
    </div>

    <!-- 右侧配置 -->
    <aside class="config-column">
      <section class="panel sticky-panel">
        <h2>AI出题配置</h2>

        <div class="metric-row">
          <div>
            <span>题库题量</span>
            <strong>{{ stats.question_count }}道</strong>
          </div>
          <div>
            <span>知识点数</span>
            <strong>{{ stats.knowledge_count }}个</strong>
          </div>
        </div>

        <label class="field">
          <span>知识点</span>
          <select v-model="selectedTopic">
            <option v-for="kp in topics" :key="kp.id" :value="kp.topic">
              {{ kp.system }} - {{ kp.topic }}
            </option>
          </select>
        </label>

        <label class="field">
          <span>难度系数</span>
          <select>
            <option value="medium">中等</option>
            <option value="easy">简单</option>
            <option value="hard">困难</option>
          </select>
        </label>

        <div class="count-row">
          <label>生成数量：</label>
          <div class="segmented" aria-label="生成数量">
            <button
              v-for="count in [1, 3, 5]"
              :key="count"
              :class="{ active: selectedCount === count }"
              type="button"
              @click="selectedCount = count"
            >
              {{ count }}道
            </button>
          </div>
          <input
            v-model.number="selectedCount"
            type="number"
            min="1"
            max="20"
            class="count-input"
            placeholder="自定义"
          />
        </div>

        <button
          class="primary-button full"
          type="button"
          :disabled="loading"
          @click="generateQuestion"
        >
          {{ loading ? "生成中..." : "AI自动生成试题" }}
        </button>

        <!-- 进度条 -->
        <div v-if="progressMsg" class="progress-bar">
          <div class="progress-inner" :class="{ done: progressMsg.startsWith('✓'), error: progressMsg.startsWith('✗') }">
            <span v-if="loading" class="spinner"></span>
            {{ progressMsg }}
          </div>
        </div>

        <section v-if="stem" class="preview-card">
          <div class="section-heading compact">
            <span class="dot blue"></span>
            <h3>实时预览</h3>
          </div>
          <p>{{ stem.slice(0, 150) }}{{ stem.length > 150 ? "..." : "" }}</p>
          <ol type="A">
            <li v-for="opt in options" :key="opt">{{ opt }}</li>
          </ol>
          <strong v-if="answer">答案：{{ answer }}</strong>
        </section>
      </section>
    </aside>
  </div>
</template>

<style scoped>
.count-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.count-row label {
  font-size: 13px;
  color: #6e7b8f;
  white-space: nowrap;
}

.count-input {
  width: 60px;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  text-align: center;
  font-size: 13px;
}

.question-nav {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
}

.nav-info {
  font-size: 14px;
  font-weight: 600;
  color: #172033;
}

.remove-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.remove-btn:hover {
  background: #fff0f0;
  border-color: #c54858;
}

.answer-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}

.answer-row label {
  font-size: 13px;
  color: #6e7b8f;
  font-weight: 600;
}

.answer-row select {
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 12px;
  font-size: 14px;
  font-weight: 700;
  color: #1385f8;
}

.explanation-input {
  width: 100%;
  min-height: 120px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 12px;
  font-size: 14px;
  line-height: 1.7;
  color: #435269;
  resize: vertical;
}

.save-section {
  display: flex;
  align-items: center;
  gap: 14px;
}

.save-hint {
  font-size: 13px;
  color: #6e7b8f;
}

.explanation-text {
  color: #435269;
  font-size: 14px;
  line-height: 1.7;
  margin: 0;
}

.button-row {
  display: flex;
  gap: 10px;
}

.candidate-placeholder {
  display: grid;
  place-items: center;
  aspect-ratio: 4 / 3;
  background: #edf2f7;
  color: #5f7087;
  font-size: 32px;
  font-weight: 700;
}

.progress-bar {
  margin-top: 10px;
}

.progress-inner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 7px;
  background: #eff8ff;
  color: #0571dc;
  font-size: 13px;
  font-weight: 600;
}

.progress-inner.done {
  background: #f0fff8;
  color: #087c55;
}

.progress-inner.error {
  background: #fff0f0;
  color: #c54858;
}

.spinner {
  width: 14px;
  height: 14px;
  border: 2px solid #b9ddff;
  border-top-color: #0571dc;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
