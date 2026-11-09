import test from 'node:test';
import assert from 'node:assert/strict';
import { standardSectionOrder,imagingFirstOrder,validPresentation,presentationPayload,sectionName } from './presentation.js';

test('presentation has no implicit default and copies an explicit knowledge-only choice',()=>{
 assert.equal(presentationPayload('direct',{section_order:standardSectionOrder()}),undefined);
 assert.equal(presentationPayload('knowledge',null),undefined);
 const choice={section_order:imagingFirstOrder()},payload=presentationPayload('knowledge',choice);
 choice.section_order.reverse();assert.deepEqual(payload.section_order,imagingFirstOrder());
 assert.equal(validPresentation(payload),true);assert.equal(validPresentation({section_order:['lab_results']}),false);
 const duplicate=standardSectionOrder();duplicate[1]=duplicate[0];assert.equal(validPresentation({section_order:duplicate}),false);
 assert.equal(validPresentation(null),true);assert.equal(sectionName('chief_complaint'),'主诉');
});
