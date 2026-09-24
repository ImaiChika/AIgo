import test from "node:test";
import assert from "node:assert/strict";
import { batchWorkspaceKey, loadBatchWorkspace, saveBatchWorkspace, clearBatchWorkspace } from "./batchWorkspace.js";

test("batch current task survives reload and is isolated by account", () => {
  const values = new Map();
  const storage = { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
  assert.equal(saveBatchWorkspace("teacher-a", "job-1", storage), true);
  assert.equal(loadBatchWorkspace("teacher-a", storage)?.jobId, "job-1");
  assert.equal(loadBatchWorkspace("teacher-b", storage), null);
  clearBatchWorkspace("teacher-a", storage);
  assert.equal(values.has(batchWorkspaceKey("teacher-a")), false);
});
