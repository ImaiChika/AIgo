export function isBasicMedicalPoint(point={}) {
 const category=(point.category||'').trim();
 if(category.startsWith('基础医学'))return true;
 if(category&&category!=='未分类')return false;
 return ['生理','生理学','生物化学','生物化学与分子生物学','病理','病理学','病理生理学','药理','药理学','医学免疫学','医学微生物学','人体解剖学','组织学与胚胎学'].includes((point.subject||'').trim());
}
