import test from "node:test";
import assert from "node:assert/strict";
import { buildSelectedTree, isWithinPath, pathKey, selectedCountsByPath } from "./knowledgePickerTree.js";

test("selected counts follow exact directory paths, even when labels repeat", () => {
  const points = [
    { id: "a", category: "临床", subject: "内科", unit: "感染", sub_item: "肺炎" },
    { id: "b", category: "临床", subject: "外科", unit: "感染", sub_item: "肺炎" },
    { id: "c", category: "临床", subject: "内科", unit: "感染", sub_item: "结核" },
  ];
  const counts = selectedCountsByPath(points);
  assert.equal(counts.get(pathKey(["临床"])), 3);
  assert.equal(counts.get(pathKey(["临床", "内科", "感染"])), 2);
  assert.equal(counts.get(pathKey(["临床", "外科", "感染"])), 1);
  assert.deepEqual(points.filter(point => isWithinPath(point, ["临床", "内科"])).map(p => p.id), ["a", "c"]);
});

test("selected tree groups thousands of points without flattening them into labels", () => {
  const points = Array.from({ length: 1200 }, (_, index) => ({
    id: `kp-${index}`, category: "临床综合", subject: index < 600 ? "内科" : "外科",
    unit: `单元${Math.floor(index / 100)}`, sub_item: "", topic: `要点${index}`,
  }));
  const tree = buildSelectedTree(points);
  assert.equal(tree.length, 1);
  assert.equal(tree[0].count, 1200);
  assert.equal(tree[0].children.length, 2);
  assert.equal(tree[0].children[0].count, 600);
  assert.equal(tree[0].children[0].children[0].points.length, 100);
});

test("points with missing unit or sub-item remain in the correct parent", () => {
  const tree = buildSelectedTree([
    { id: "a", category: "医学", subject: "内科", unit: "", sub_item: "" },
    { id: "b", category: "医学", subject: "内科", unit: "呼吸", sub_item: "" },
    { id: "c", category: "医学", subject: "内科", unit: "", sub_item: "少见细目" },
  ]);
  assert.deepEqual(tree[0].children[0].points.map(point => point.id), ["a"]);
  assert.deepEqual(tree[0].children[0].children[0].points.map(point => point.id), ["b"]);
  assert.deepEqual(tree[0].children[0].children[1].children[0].points.map(point => point.id), ["c"]);
});
