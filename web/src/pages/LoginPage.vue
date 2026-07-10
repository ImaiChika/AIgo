<script setup>
import { ref } from "vue";
import { useRouter } from "vue-router";
import { api } from "../api.js";
import { setAuth } from "../auth.js";

const router = useRouter();
const username = ref("");
const password = ref("");
const error = ref("");
const loading = ref(false);

async function doLogin() {
  if (!username.value || !password.value) {
    error.value = "请输入用户名和密码";
    return;
  }
  loading.value = true;
  error.value = "";
  try {
    const data = await api.login(username.value, password.value);
    setAuth(data.token, data.user);
    router.push("/generate");
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <div class="brand-mark">A</div>
        <h1>AIgo</h1>
        <p>智能命题系统</p>
      </div>

      <form @submit.prevent="doLogin">
        <div class="field">
          <label>用户名</label>
          <input v-model="username" type="text" placeholder="请输入用户名" autocomplete="username" />
        </div>
        <div class="field">
          <label>密码</label>
          <input v-model="password" type="password" placeholder="请输入密码" autocomplete="current-password" />
        </div>
        <div v-if="error" class="error">{{ error }}</div>
        <button class="primary-button full" type="submit" :disabled="loading">
          {{ loading ? "登录中..." : "登录" }}
        </button>
      </form>

      <div class="login-hint">
        默认管理员：admin / admin
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  display: grid;
  place-items: center;
  min-height: 100vh;
  background: #f3f6fb;
}

.login-card {
  width: 380px;
  padding: 40px;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 12px 28px rgba(24, 39, 75, 0.1);
}

.login-brand {
  text-align: center;
  margin-bottom: 32px;
}

.brand-mark {
  display: inline-grid;
  place-items: center;
  width: 56px;
  height: 56px;
  border-radius: 12px;
  background: #1385f8;
  color: #fff;
  font-size: 28px;
  font-weight: 800;
  margin-bottom: 12px;
}

.login-brand h1 {
  margin: 0;
  font-size: 24px;
  color: #172033;
}

.login-brand p {
  margin: 4px 0 0;
  color: #6e7b8f;
  font-size: 14px;
}

.field {
  margin-bottom: 16px;
}

.field label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.field input {
  width: 100%;
  height: 40px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 12px;
  font-size: 14px;
  color: #172033;
}

.field input:focus {
  outline: none;
  border-color: #1385f8;
  box-shadow: 0 0 0 3px rgba(19, 133, 248, 0.1);
}

.error {
  padding: 8px 12px;
  background: #fff0f0;
  color: #c54858;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 16px;
}

.primary-button {
  height: 42px;
  border: 0;
  border-radius: 7px;
  background: #1385f8;
  color: #fff;
  font-size: 15px;
  font-weight: 600;
  cursor: pointer;
}

.primary-button:hover {
  background: #0571dc;
}

.primary-button:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.full {
  width: 100%;
}

.login-hint {
  margin-top: 20px;
  text-align: center;
  font-size: 12px;
  color: #6e7b8f;
}
</style>
