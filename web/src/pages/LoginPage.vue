<script setup>
import { onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { api } from "../api.js";
import { setAuth } from "../auth.js";

const router = useRouter();
const mode = ref("login"); // login | register
const registerEnabled = ref(false);
const username = ref("");
const password = ref("");
const confirmPassword = ref("");
const displayName = ref("");
const error = ref("");
const success = ref("");
const loading = ref(false);

onMounted(async () => {
  try {
    const res = await fetch("/api/auth/register-enabled");
    const data = await res.json();
    registerEnabled.value = !!data.enabled;
  } catch (e) {
    registerEnabled.value = false;
  }
});

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
    router.push("/my");
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
}

async function doRegister() {
  error.value = "";
  success.value = "";
  if (!username.value || !password.value) {
    error.value = "请输入用户名和密码";
    return;
  }
  if (password.value.length < 8) {
    error.value = "密码至少8位";
    return;
  }
  if (password.value !== confirmPassword.value) {
    error.value = "两次输入的密码不一致";
    return;
  }
  loading.value = true;
  try {
    await api.register(username.value, password.value, displayName.value);
    success.value = "注册成功，请等待管理员分配权限后登录";
    mode.value = "login";
    password.value = "";
    confirmPassword.value = "";
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
}

function switchMode(m) {
  mode.value = m;
  error.value = "";
  success.value = "";
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <img class="login-logo" src="/zhique-logo.png" alt="治趣" />
        <h1>AI医学试题生成系统</h1>
      </div>

      <div v-if="mode === 'login'">
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
          <div v-if="success" class="success">{{ success }}</div>
          <button class="primary-button full" type="submit" :disabled="loading">
            {{ loading ? "登录中..." : "登录" }}
          </button>
        </form>
        <p class="switch-hint" v-if="registerEnabled">
          没有账号？
          <a href="#" @click.prevent="switchMode('register')">注册新账号</a>
        </p>
      </div>

      <div v-else>
        <form @submit.prevent="doRegister">
          <div class="field">
            <label>用户名</label>
            <input v-model="username" type="text" placeholder="登录用户名" autocomplete="username" />
          </div>
          <div class="field">
            <label>显示名</label>
            <input v-model="displayName" type="text" placeholder="真实姓名（可选）" />
          </div>
          <div class="field">
            <label>密码</label>
            <input v-model="password" type="password" placeholder="至少8位" autocomplete="new-password" />
          </div>
          <div class="field">
            <label>确认密码</label>
            <input v-model="confirmPassword" type="password" placeholder="再次输入密码" autocomplete="new-password" />
          </div>
          <div v-if="error" class="error">{{ error }}</div>
          <button class="primary-button full" type="submit" :disabled="loading">
            {{ loading ? "注册中..." : "注册" }}
          </button>
        </form>
        <p class="switch-hint">
          已有账号？
          <a href="#" @click.prevent="switchMode('login')">返回登录</a>
        </p>
        <p class="switch-hint note">注册后默认无任何权限，请联系管理员在「用户管理」中分配权限。</p>
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

.login-logo {
  display: block;
  width: min(100%, 300px);
  height: auto;
  margin: 0 auto 16px;
}

.login-brand h1 {
  margin: 0;
  font-size: 24px;
  color: #172033;
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
  box-sizing: border-box;
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

.success {
  padding: 8px 12px;
  background: #f0fff8;
  color: #087c55;
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

.switch-hint {
  margin: 16px 0 0;
  text-align: center;
  font-size: 13px;
  color: #6e7b8f;
}

.switch-hint a {
  color: #1385f8;
  text-decoration: none;
}

.switch-hint.note {
  font-size: 12px;
  color: #9aa5b4;
}

@media (max-width: 460px) {
  .login-card {
    width: calc(100vw - 28px);
    padding: 28px 22px;
  }

  .login-logo {
    width: min(100%, 280px);
  }
}

</style>
