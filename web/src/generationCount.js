export function validGenerationCount(value) {
  const count = Number(value);
  return Number.isInteger(count) && count >= 1 && count <= 20;
}
