import { onMounted, onBeforeUnmount } from 'vue';
import { createLiveRefresh } from './liveRefresh.js';
let coordinator;
export function useLiveRefresh(callback) {
 let unsubscribe;
 onMounted(() => {
   coordinator ||= createLiveRefresh({ document, window });
   unsubscribe = coordinator.subscribe(callback);
 });
 onBeforeUnmount(() => unsubscribe?.());
}
