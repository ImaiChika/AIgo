export const clinicalSections = Object.freeze([
  ['chief_complaint','主诉'], ['present_illness','现病史'], ['past_history','相关既往史'],
  ['vitals','生命征'], ['physical_findings','查体'], ['lab_results','实验室'],
  ['imaging_results','影像'], ['other','其他相关信息'],
].map(([id,label])=>Object.freeze({id,label})));

export const sectionName = id => clinicalSections.find(s=>s.id===id)?.label || '病例信息';
export const standardSectionOrder = () => clinicalSections.map(s=>s.id);
export const imagingFirstOrder = () => {
  const order=standardSectionOrder();[order[5],order[6]]=[order[6],order[5]];return order;
};
export function validPresentation(value) {
  if(value==null)return true;
  const order=value.section_order;
  return Array.isArray(order)&&order.length===clinicalSections.length&&new Set(order).size===order.length&&order.every(id=>clinicalSections.some(s=>s.id===id));
}
export function presentationPayload(mode,value) {
  return mode==='knowledge'&&value?{section_order:[...value.section_order]}:undefined;
}
