export const pathKey = (path) => JSON.stringify(path);

export function pointPath(point) {
  return [point.category || "", point.subject || "", point.unit || "", point.sub_item || ""];
}

export function isWithinPath(point, path) {
  const values = pointPath(point);
  return path.every((value, index) => values[index] === value);
}

export function selectedCountsByPath(points) {
  const counts = new Map();
  for (const point of points) {
    const path = pointPath(point);
    for (let depth = 1; depth <= 4; depth++) {
      const key = pathKey(path.slice(0, depth));
      counts.set(key, (counts.get(key) || 0) + 1);
    }
  }
  return counts;
}

// 只保留目录节点与当前层直属考点。界面按层展开、按页渲染，避免上千标签同屏。
export function buildSelectedTree(points) {
  const roots = [];
  const nodes = new Map();
  for (const point of points) {
    const values = pointPath(point);
    let children = roots;
    for (let depth = 0; depth < 4; depth++) {
      // 大纲中单元、细目可以为空；考点归入最后一个实际存在的目录。
      if (depth >= 2 && values.slice(depth).every(value => !value)) break;
      const path = values.slice(0, depth + 1);
      const key = pathKey(path);
      let node = nodes.get(key);
      if (!node) {
        node = {
          key, path, label: values[depth] || ["未分类", "未设置专业", "未设置目录", "未设置目录"][depth],
          count: 0, children: [], points: [],
        };
        nodes.set(key, node);
        children.push(node);
      }
      node.count++;
      if (depth === 3 || values.slice(depth + 1).every(value => !value)) node.points.push(point);
      children = node.children;
    }
  }
  return roots;
}
