import test from 'node:test';
import assert from 'node:assert/strict';
import {isBasicMedicalPoint} from './generationPolicy.js';
test('basic outline selection is blocked while clinical mechanisms remain eligible',()=>{
 assert.equal(isBasicMedicalPoint({category:'基础医学',subject:'病理学'}),true);
 assert.equal(isBasicMedicalPoint({subject:'生理学'}),true);
 assert.equal(isBasicMedicalPoint({category:'临床综合',subject:'呼吸系统',topic:'肺炎的发病机制'}),false);
 assert.equal(isBasicMedicalPoint({category:'临床综合',subject:'药理学'}),false);
});
