import test from "node:test";
import assert from "node:assert/strict";
import { batchWorkspaceKey, loadBatchWorkspace, saveBatchWorkspace, clearBatchWorkspace, batchUnitRows } from "./batchWorkspace.js";

test("out-of-order progress keeps unit positions and does not display pending work as failures", () => {
  const rows = batchUnitRows([
    { status: "pending", outline_code: "a" },
    { status: "ok", outline_code: "b" },
    { status: "running", outline_code: "c" },
    { status: "failed", outline_code: "d", error: "timeout" },
  ]);
  assert.deepEqual(rows.map(row => row.idx), [2, 4]);
  assert.equal(rows[0].ok, true);
  assert.equal(rows[0].error, "");
  assert.equal(rows[1].error, "timeout");
  assert.deepEqual(batchUnitRows(), []);
});

test("batch current task survives reload and is isolated by account", () => {
  const values = new Map();
  const storage = { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
  assert.equal(saveBatchWorkspace("teacher-a", "job-1", storage), true);
  assert.equal(loadBatchWorkspace("teacher-a", storage)?.jobId, "job-1");
  assert.equal(loadBatchWorkspace("teacher-b", storage), null);
  clearBatchWorkspace("teacher-a", storage);
  assert.equal(values.has(batchWorkspaceKey("teacher-a")), false);
});

test("pipeline phase keeps generation and first check separate", async () => {
  const { batchPipelinePhase } = await import("./batchWorkspace.js");
  assert.equal(batchPipelinePhase({pipeline:true,total_count:4,completed:1,failed:0,pipeline_result:{saved:1}}, {total:1,passed:1,checking:0}), "生成与首检进行中");
  assert.equal(batchPipelinePhase({pipeline:true,total_count:4,completed:4,failed:0,pipeline_result:{saved:4}}, {total:4,passed:3,checking:1}), "首检进行中");
  assert.equal(batchPipelinePhase({pipeline:true,total_count:4,completed:3,failed:1,pipeline_result:{saved:3}}, {total:3,passed:3,checking:0}), "部分完成");
});

test("out-of-order early results keep the original unit number", async () => {
  const { batchResultRows } = await import("./batchWorkspace.js");
  const first = batchResultRows({question_ids:["q3"],items:[{status:"pending"},{status:"pending"},{status:"ok",question_id:"q3"}]}, {items:[{question_id:"q3",task_status:"succeeded",verdict:"pass",question_status:"ai_reviewed",stem_summary:"passed"}]});
  assert.equal(first[0].no,3);
  const later = batchResultRows({question_ids:["q1","q3"],items:[{status:"ok",question_id:"q1"},{status:"pending"},{status:"ok",question_id:"q3"}]}, {items:[]});
  assert.deepEqual(later.map(row=>row.no),[1,3]);
});

test("pipeline pending slots are never reported as failed imports", async () => {
  const { batchGenerationFailures } = await import("./batchWorkspace.js");
  assert.deepEqual(batchGenerationFailures({items:[{status:"pending"},{status:"running"},{status:"ok"},{status:"failed",error:"generation failed"}]}).map(item=>item.status),["failed"]);
});
