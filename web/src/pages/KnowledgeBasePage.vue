<script setup>
import {ref,computed,watch} from 'vue';import {currentUser} from '../auth.js';import {collectionCapabilities} from '../knowledgeCollection.js';
import SharedKnowledgeCardsPage from './SharedKnowledgeCardsPage.vue';import KnowledgeSubmissions from '../components/KnowledgeSubmissions.vue';
const tab=ref('cards'),caps=computed(()=>collectionCapabilities(currentUser.value?.permissions));
watch(()=>caps.value.review,value=>{if(!value)tab.value='cards';});
</script>
<template><div class="knowledge-workspace"><nav v-if="caps.review" class="collection-tabs" aria-label="共享知识库模块"><button :class="{active:tab==='cards'}" @click="tab='cards'">知识卡</button><button :class="{active:tab==='review'}" @click="tab='review'">知识卡审核</button></nav><SharedKnowledgeCardsPage v-if="tab==='cards'"/><KnowledgeSubmissions v-else :review="true"/></div></template>
