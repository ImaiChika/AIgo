<script setup>
import {ref,computed,watch} from 'vue';import {currentUser} from '../auth.js';
import PersonalRulesPage from './PersonalRulesPage.vue';import PrivateKnowledgeCards from '../components/PrivateKnowledgeCards.vue';import KnowledgeSubmissions from '../components/KnowledgeSubmissions.vue';
const tab=ref('rules'),cards=ref(null),rules=ref(null);const identity=computed(()=>JSON.stringify([currentUser.value?.id,currentUser.value?.role,currentUser.value?.permissions?.slice().sort()]));
watch(identity,()=>{tab.value='rules';});
function change(value){if(cards.value?.allowLeaving&&!cards.value.allowLeaving()||rules.value?.allowLeaving&&!rules.value.allowLeaving())return;tab.value=value;}
</script>
<template><div class="knowledge-workspace"><nav class="collection-tabs" aria-label="个人知识库模块"><button :class="{active:tab==='rules'}" @click="change('rules')">命题规则</button><button :class="{active:tab==='cards'}" @click="change('cards')">知识卡</button><button :class="{active:tab==='submissions'}" @click="change('submissions')">我的申请</button></nav><PersonalRulesPage v-if="tab==='rules'" ref="rules"/><PrivateKnowledgeCards v-else-if="tab==='cards'" ref="cards"/><KnowledgeSubmissions v-else/></div></template>
