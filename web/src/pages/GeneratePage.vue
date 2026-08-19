<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";
import { getToken } from "../auth.js";
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";

// 知识点选择（单选，使用完善的知识点选择器：精确+模糊搜索）
const selectedKPs = ref([]); // 单选模式下始终 0/1 个
const selectedKP = computed(() => (selectedKPs.value.length ? selectedKPs.value[0] : null));

// 出题配置
const selectedCount = ref(1);
const selectedDifficulty = ref("0.65");
const needImages = ref(false);
const imageCount = ref(3);
const banks = ref([]);
const selectedBank = ref("");

// 状态
const toast = ref("");
const loading = ref(false);
const progressMsg = ref("");
const startTime = ref(0);
const stats = ref({ question_count: 0, knowledge_count: 0 });

// 生成结果
const generatedQuestions = ref([]);
const currentIndex = ref(0);
const currentQuestionId = ref("");
const stem = ref("");
const options = ref([]);
const answer = ref("");
const explanation = ref("");
let optionIdCounter = 0;

// 配图
const imageLoading = ref(false);
const imageProgress = ref("");
const generatedImages = ref([]);
const selectedImage = ref(-1);
const promptText = ref("");

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadStats() {
  try {
    stats.value = await api.stats();
  } catch (e) {
    console.error(e);
    showToast("加载统计信息失败: " + e.message);
  }
}

// 生成题目
async function generateQuestion() {
  if (!selectedKP.value) {
    showToast("请先选择知识点");
    return;
  }
  loading.value = true;
  progressMsg.value = "正在调用千问生成试题，请稍候...";
  startTime.value = Date.now();

  const timer = setInterval(() => {
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(0);
    progressMsg.value = `千问生成中... 已等待 ${elapsed} 秒`;
  }, 1000);

  try {
    const genParams = {
      subject: selectedKP.value.subject || "临床医学",
      category: selectedKP.value.category || "",
      difficulty: selectedDifficulty.value,
      topic: selectedKP.value.topic,
      outline_code: selectedKP.value.outline_code || selectedKP.value.id || "",
      count: selectedCount.value,
      bank_id: selectedBank.value,
    };
    console.log("生成参数:", genParams, "选中知识点:", JSON.stringify(selectedKP.value));
    const data = await api.generate(genParams);
    clearInterval(timer);
    const elapsed = ((Date.now() - startTime.value) / 1000).toFixed(1);
    if (data.questions && data.questions.length > 0) {
      generatedQuestions.value = data.questions;
      generatedQuestions.value.forEach(q => { q.images = []; });
      currentIndex.value = 0;
      showQuestion(0);
      progressMsg.value = `✓ 已生成 ${data.count} 道题，耗时 ${elapsed} 秒`;
      showToast(`已生成 ${data.count} 道题，耗时 ${elapsed} 秒`);
      loadStats();

      if (needImages.value) {
        await generateImagesForAll();
      }
    }
  } catch (e) {
    clearInterval(timer);
    progressMsg.value = `✗ 生成失败: ${e.message}`;
    showToast("生成失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

// 为所有题目生成配图
async function generateImagesForAll() {
  imageLoading.value = true;
  const total = generatedQuestions.value.length;
  let done = 0;

  for (const q of generatedQuestions.value) {
    if (!q.id) continue;
    imageProgress.value = `正在为第 ${done + 1}/${total} 道题生成配图...`;
    try {
      await api.imagePrompt(q.id);
      const imgData = await api.imageGenerate(q.id, imageCount.value);
      q.images = imgData.images || [];
    } catch (e) {
      q.images = [];
      q.imageError = e.message;
    }
    done++;
    imageProgress.value = `配图进度: ${done}/${total}`;
  }

  imageProgress.value = `✓ 配图完成: ${done} 道题`;
  imageLoading.value = false;
  showQuestion(currentIndex.value);
}

function showQuestion(index) {
  if (index < 0 || index >= generatedQuestions.value.length) return;
  currentIndex.value = index;
  const q = generatedQuestions.value[index];
  stem.value = q.clinical_stem || "";
  options.value = (q.options || []).map((o) => ({ id: ++optionIdCounter, text: o.text }));
  answer.value = q.answer || "";
  explanation.value = q.explanation || "";
  currentQuestionId.value = q.id || "";
  generatedImages.value = q.images || [];
  selectedImage.value = generatedImages.value.length > 0 ? 0 : -1;
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

function addOption() {
  if (options.value.length >= 5) {
    showToast("A2 单选题最多保留 A-E 五个选项");
    return;
  }
  options.value.push({ id: ++optionIdCounter, text: "新增选项" });
}

function optionLabel(index) {
  return String.fromCharCode(65 + index);
}

async function saveQuestion() {
  if (!currentQuestionId.value) {
    showToast("没有可保存的题目");
    return;
  }
  try {
    const data = await api.updateQuestion(currentQuestionId.value, {
      clinical_stem: stem.value,
      options: options.value.map((opt, i) => ({ label: String.fromCharCode(65 + i), text: opt.text })),
      answer: answer.value,
      explanation: explanation.value,
    });
    if (generatedQuestions.value[currentIndex.value]) {
      generatedQuestions.value[currentIndex.value] = data;
    }
    showToast("已保存修改");
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
  imageProgress.value = "正在调用生图模型，请稍候...";
  const startTimeImg = Date.now();

  const timer = setInterval(() => {
    const elapsed = ((Date.now() - startTimeImg) / 1000).toFixed(0);
    imageProgress.value = `生图中... 已等待 ${elapsed} 秒`;
  }, 1000);

  try {
    const data = await api.imageGenerate(currentQuestionId.value, 4);
    clearInterval(timer);
    const elapsed = ((Date.now() - startTimeImg) / 1000).toFixed(1);
    generatedImages.value = data.images || [];
    selectedImage.value = 0;
    imageProgress.value = `✓ 已生成 ${data.count} 张候选图，耗时 ${elapsed} 秒`;
    showToast(`已生成 ${data.count} 张候选图`);
  } catch (e) {
    clearInterval(timer);
    imageProgress.value = `✗ 生成失败: ${e.message}`;
    showToast("生成图片失败: " + e.message);
  } finally {
    imageLoading.value = false;
  }
}

function selectImage(index) {
  selectedImage.value = index;
  showToast(`已选择候选图 ${index + 1}`);
}

function imageSrc(path) {
  if (!path) return "";
  const filename = path.split("/").pop();
  const token = getToken();
  return `/images/${filename}${token ? `?token=${encodeURIComponent(token)}` : ""}`;
}

async function loadBanks() {
  try {
    const data = await api.listBanks();
    banks.value = data.banks || [];
  } catch (e) {
    console.error(e);
  }
}

onMounted(() => {
  loadStats();
  loadBanks();
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
          <div v-for="(opt, index) in options" :key="opt.id" class="option-row">
            <span class="option-label">{{ optionLabel(index) }}</span>
            <input v-model="opt.text" />
            <button class="remove-btn" type="button" @click="removeOption(index)" :disabled="options.length <= 4" title="删除选项">×</button>
          </div>
        </div>
        <button class="text-button" type="button" @click="addOption">+ 添加选项</button>

        <div class="answer-row">
          <label>正确答案：</label>
          <select v-model="answer">
            <option v-for="(opt, i) in options" :key="opt.id" :value="String.fromCharCode(65 + i)">
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

      <!-- 已入库提示 -->
      <section v-if="currentQuestionId" class="panel save-section">
        <span class="save-hint">✓ 题目已自动入库，可在题库页面查看和编辑</span>
        <button class="ghost-button" type="button" @click="saveQuestion">保存修改</button>
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
            <button class="outline-button" type="button" @click="generatePrompt" :disabled="!currentQuestionId">生成提示词</button>
            <button class="outline-button" type="button" @click="generateImages" :disabled="!currentQuestionId || imageLoading">
              {{ imageLoading ? "生成中..." : "生成4张候选图" }}
            </button>
          </div>
        </div>

        <div v-if="imageProgress" class="progress-bar">
          <div class="progress-inner" :class="{ done: imageProgress.startsWith('✓'), error: imageProgress.startsWith('✗') }">
            <span v-if="imageLoading" class="spinner"></span>
            {{ imageProgress }}
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
            <img v-if="img.image_path" :src="imageSrc(img.image_path)" class="candidate-img" :alt="`候选图${index+1}`" />
            <div v-else class="candidate-placeholder">
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

        <!-- 知识点选择器（精确+模糊搜索，单选） -->
        <div class="kp-selector">
          <label class="field-label">选择知识点</label>
          <KnowledgePointPicker v-model="selectedKPs" :multiple="false" placeholder="搜索知识点、大纲代码、专业...（支持精确筛选与模糊搜索）" />
        </div>

        <label class="field">
          <span>难度系数</span>
          <select v-model="selectedDifficulty">
            <option value="0.55">简单 (0.55)</option>
            <option value="0.65">中等 (0.65)</option>
            <option value="0.75">偏难 (0.75)</option>
            <option value="0.85">困难 (0.85)</option>
          </select>
        </label>

        <label class="field" v-if="banks.length">
          <span>目标题库</span>
          <select v-model="selectedBank">
            <option value="">未分类</option>
            <option v-for="b in banks" :key="b.id" :value="b.id">{{ b.name }}</option>
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

        <!-- 配图选项 -->
        <div class="image-options">
          <label class="checkbox-row">
            <input type="checkbox" v-model="needImages" />
            <span>需要配图</span>
          </label>
          <div v-if="needImages" class="image-count-row">
            <label>每题图片数：</label>
            <select v-model.number="imageCount">
              <option :value="1">1张</option>
              <option :value="2">2张</option>
              <option :value="3">3张</option>
              <option :value="4">4张</option>
              <option :value="5">5张</option>
            </select>
          </div>
        </div>

        <button
          class="primary-button full"
          type="button"
          :disabled="loading || !selectedKP"
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
            <li v-for="opt in options" :key="opt.id">{{ opt.text }}</li>
          </ol>
          <strong v-if="answer">答案：{{ answer }}</strong>
        </section>
      </section>
    </aside>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
/* 知识点选择器 */
.kp-selector {
  margin-bottom: 16px;
}

.field-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.selected-kp {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  background: #eff8ff;
  border: 1px solid #b9ddff;
  border-radius: 8px;
}

.kp-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.kp-code {
  font-size: 11px;
  font-family: monospace;
  color: #0571dc;
}

.kp-subject {
  font-size: 11px;
  color: #6e7b8f;
}

.kp-topic {
  font-size: 13px;
  color: #172033;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.clear-btn {
  width: 24px;
  height: 24px;
  border: 1px solid #b9ddff;
  border-radius: 50%;
  background: #fff;
  color: #0571dc;
  font-size: 16px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.clear-btn:hover {
  background: #0571dc;
  color: #fff;
}

.kp-search-box {
  position: relative;
}

.kp-search-input {
  width: 100%;
  height: 38px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 0 12px;
  font-size: 13px;
}

.kp-search-input:focus {
  border-color: #1385f8;
  outline: none;
  box-shadow: 0 0 0 3px rgba(19, 133, 248, 0.1);
}

.kp-dropdown {
  position: fixed;
  top: auto;
  left: auto;
  width: 300px;
  margin-top: 4px;
  background: #fff;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  z-index: 1000;
  max-height: 400px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.kp-list {
  overflow-y: auto;
  max-height: 350px;
}

.kp-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font-size: 13px;
  border-bottom: 1px solid #f0f3f7;
}

.kp-item:hover {
  background: #f8fbff;
}

.kp-item-code {
  font-size: 10px;
  font-family: monospace;
  color: #0571dc;
  background: #eff8ff;
  padding: 2px 6px;
  border-radius: 3px;
  flex-shrink: 0;
}

.kp-item-subject {
  font-size: 11px;
  color: #6e7b8f;
  flex-shrink: 0;
}

.kp-item-topic {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kp-loading, .kp-empty {
  padding: 20px;
  text-align: center;
  color: #6e7b8f;
  font-size: 13px;
}

.kp-load-more {
  padding: 8px;
  text-align: center;
  border-top: 1px solid #f0f3f7;
}

.dropdown-overlay {
  position: fixed;
  inset: 0;
  z-index: 150;
}

/* 其他样式 */
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

.remove-btn:hover:not(:disabled) {
  background: #fff0f0;
  border-color: #c54858;
}

.remove-btn:disabled {
  opacity: 0.3;
  cursor: not-allowed;
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

.button-row {
  display: flex;
  gap: 10px;
}

.candidate-img {
  width: 100%;
  aspect-ratio: 4 / 3;
  object-fit: cover;
  display: block;
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

.image-options {
  margin-top: 10px;
}

.checkbox-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 14px;
  color: #172033;
}

.checkbox-row input[type="checkbox"] {
  width: 16px;
  height: 16px;
}

.image-count-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 13px;
  color: #6e7b8f;
}

.image-count-row select {
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}
</style>
