import {knowledgeCapabilities} from './knowledgeBaseAccess.js';
import {learningCapabilities} from './personalLearning.js';
export function collectionCapabilities(permissions=[]){const kb=knowledgeCapabilities(permissions),privateCaps=learningCapabilities(permissions);return {view:privateCaps.libraryView,manage:privateCaps.manage,use:privateCaps.libraryView&&privateCaps.use,submit:kb.view&&permissions.includes('kb:submit')&&privateCaps.libraryView,review:kb.view&&permissions.includes('kb:review'),workflow:kb.use||privateCaps.libraryView&&privateCaps.use};}
export const submissionStates={pending:'待审核',approved:'已通过',rejected:'已驳回'};
export function stableOperation(store,kind,payload){const hash=JSON.stringify([kind,payload]);if(store.value?.hash!==hash)store.value={hash,key:crypto.randomUUID()};return store.value.key;}
