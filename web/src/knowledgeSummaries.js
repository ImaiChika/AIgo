// A summary has its own API envelope and marker. Never turn one into a save body.
export function knowledgeRows(data){return data?.view==='summary'?(data.items||[]):(data?.cards||[]);}
export function knowledgeExcerpt(card){return card.is_summary?card.excerpt:(card.content?.expression?.instructions||card.content?.expression?.explanation_note||card.content?.custom_text||card.content?.facts?.[0]?.text||card.content?.reference?.stem||'');}
export async function completeKnowledgeCards(cards,read,active=()=>true){
 const check=()=>{if(!active()){const e=new Error("工作身份或权限已变化，请刷新后重试。");e.staleSession=true;throw e;}};
 const result=[];
 for(let offset=0;offset<cards.length;offset+=4){
  check();
  const group=await Promise.all(cards.slice(offset,offset+4).map(async card=>{
   if(!card.is_summary)return JSON.parse(JSON.stringify(card));
   const full=await read(card.id);
   if(!full||full.is_summary||!full.content||full.id!==card.id){const e=new Error('未收到完整知识卡，请保留当前内容并重试。');e.status=502;throw e;}
   if(full.revision!==card.revision){const e=new Error(`知识卡「${card.title}」已更新，请刷新列表后重试。`);e.status=409;throw e;}
   if(card.has_expression&&!full.content.expression){const e=new Error('知识卡详情缺少表达要求，请保留当前内容，确认服务已更新后重试。');e.status=502;throw e;}
   return JSON.parse(JSON.stringify(full));
  }));check();result.push(...group);
 }
 return result;
}
