export function splitQuestionIds(ids, size = 500) {
  const chunks = [];
  for (let index = 0; index < ids.length; index += size) chunks.push(ids.slice(index, index + size));
  return chunks;
}

export function combineProgressResponses(responses) {
  const counts = {};
  const items = [];
  let total = 0;
  for (const response of responses) {
    total += Number(response.total || 0);
    for (const [key, value] of Object.entries(response.counts || {})) counts[key] = (counts[key] || 0) + Number(value || 0);
    items.push(...(response.items || []));
  }
  return { total, counts, items };
}
