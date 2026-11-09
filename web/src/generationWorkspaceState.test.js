import test from "node:test";
import assert from "node:assert/strict";
import {
  clearGenerationWorkspace,
  loadGenerationWorkspace,
  saveGenerationWorkspace,
	createGenerationQuotaEditor, refreshGenerationQuotaEditor,resetGenerationQuotaEditor,generationQuotaUpdate,generationQuotaDirty,generationQuotaFields,setGenerationModelTool,
} from "./generationWorkspaceState.js";

const quotaDefaults = Object.fromEntries(generationQuotaFields.map(field => [field.key, { single_max_questions:20,batch_max_questions:100,single_active_per_user:2,batch_active_per_user:1,global_pending_questions:500 }[field.key] ?? 2]));

test("network switches are independent drafts, dependency-safe and survive polling", () => {
  let state=refreshGenerationQuotaEditor(createGenerationQuotaEditor(),{...quotaDefaults,revision:1});
  assert.equal(state.draft.model_tools.web_search_enabled,false);
  assert.throws(()=>setGenerationModelTool(state,"web_extractor_enabled",true));
  state=setGenerationModelTool(state,"web_search_enabled",true);
  state=setGenerationModelTool(state,"web_extractor_enabled",true);
  assert.equal(state.saved.model_tools.web_search_enabled,false);
  assert.equal(generationQuotaDirty(state),true);
  state=refreshGenerationQuotaEditor(state,{...quotaDefaults,revision:2,model_tools:{web_search_enabled:false,web_extractor_enabled:false}});
  assert.equal(state.draft.model_tools.web_extractor_enabled,true);
  assert.equal(state.conflict,true);
  assert.equal(generationQuotaUpdate(state).revision,1);
  state=setGenerationModelTool(state,"web_search_enabled",false);
  assert.equal(state.draft.model_tools.web_extractor_enabled,false);
  state=resetGenerationQuotaEditor(state);
  assert.equal(generationQuotaDirty(state),false);
  assert.equal(state.baseRevision,2);
  const bad={...state,draft:{...state.draft,model_tools:{web_search_enabled:false,web_extractor_enabled:true}}};
  assert.throws(()=>generationQuotaUpdate(bad),/必须先开启/);
});

test("quota polling preserves edited inputs and their original revision", () => {
  let state = refreshGenerationQuotaEditor(createGenerationQuotaEditor(), { ...quotaDefaults,revision:1 });
  state.draft.batch_concurrency=3;
  state = refreshGenerationQuotaEditor(state,{ ...quotaDefaults,global_pending_questions:600,revision:2 });
  assert.equal(state.draft.batch_concurrency,3);
  assert.equal(state.draft.global_pending_questions,500);
  assert.equal(state.saved.global_pending_questions,600);
  assert.equal(state.conflict,true);
  assert.equal(generationQuotaUpdate(state).revision,1);
  state=resetGenerationQuotaEditor(state);
  assert.equal(state.draft.global_pending_questions,600);
  assert.equal(state.baseRevision,2);
  assert.equal(state.conflict,false);
  assert.equal(generationQuotaDirty(state),false);
});

test("idle quota polling adopts current values and concurrency validates the full range", () => {
  let state = refreshGenerationQuotaEditor(createGenerationQuotaEditor(), { ...quotaDefaults,revision:1 });
  state = refreshGenerationQuotaEditor(state,{ ...quotaDefaults,batch_concurrency:4,revision:2 });
  assert.equal(generationQuotaUpdate(state).batch_concurrency,4);
  assert.equal(generationQuotaUpdate(state).revision,2);
  for (const key of ["generation_concurrency","batch_concurrency","ai_check_concurrency"]) {
    for (const value of [0,33,1.5,"",NaN]) {
      assert.throws(() => generationQuotaUpdate({ ...state,draft:{...state.draft,[key]:value} }));
    }
  }
});

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

test("generation workspace is isolated by user and survives reload", () => {
  const storage = memoryStorage();
  assert.equal(saveGenerationWorkspace("user-a", { runId: "gen-1", generatedQuestions: [{ id: "q-1" }] }, storage), true);
  assert.equal(loadGenerationWorkspace("user-a", storage).runId, "gen-1");
  assert.equal(loadGenerationWorkspace("user-b", storage), null);
});

test("clearing a workspace only removes the selected user", () => {
  const storage = memoryStorage();
  saveGenerationWorkspace("user-a", { runId: "gen-a" }, storage);
  saveGenerationWorkspace("user-b", { runId: "gen-b" }, storage);
  clearGenerationWorkspace("user-a", storage);
  assert.equal(loadGenerationWorkspace("user-a", storage), null);
  assert.equal(loadGenerationWorkspace("user-b", storage).runId, "gen-b");
});
