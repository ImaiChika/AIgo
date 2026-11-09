import test from 'node:test';
import assert from 'node:assert/strict';
import {formatDateTime} from './dateTime.js';

test('date display has stable Chinese-interface ordering and a 24-hour clock',()=>{
 assert.equal(formatDateTime(new Date(2026,9,29,0,1,20)),'2026-10-29 00:01:20');
 assert.equal(formatDateTime(new Date(2026,9,29,15,32,52).toISOString()),'2026-10-29 15:32:52');
});
test('missing, zero-sentinel and invalid timestamps use the caller fallback',()=>{
 for(const value of [null,undefined,'','0001-01-01T00:00:00Z','invalid'])assert.equal(formatDateTime(value),'—');
 assert.equal(formatDateTime('', '未记录'),'未记录');
 assert.match(formatDateTime(0),/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
});
