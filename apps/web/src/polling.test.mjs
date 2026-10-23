import { test } from 'node:test';
import assert from 'node:assert/strict';
import { startPolling } from './polling.ts';

test('polling serializes slow requests, skips hidden pages and cleans up', async () => {
 const oldDocument=globalThis.document, oldInterval=globalThis.setInterval, oldClear=globalThis.clearInterval;
 let tick, listener, calls=0, release;
 globalThis.document={hidden:false,addEventListener:(_,cb)=>{listener=cb},removeEventListener:(_,cb)=>{assert.equal(cb,listener);listener=null}};
 globalThis.setInterval=(cb)=>{tick=cb;return 1};
 globalThis.clearInterval=(id)=>assert.equal(id,1);
 try {
  const stop=startPolling(()=>{calls++;return new Promise(r=>{release=r})},10);
  assert.equal(calls,1);tick();assert.equal(calls,1);
  release();await new Promise(r=>setImmediate(r));
  document.hidden=true;tick();assert.equal(calls,1);
  document.hidden=false;listener();assert.equal(calls,2);
  stop();release();await new Promise(r=>setImmediate(r));tick();assert.equal(calls,2);assert.equal(listener,null);
 } finally {globalThis.document=oldDocument;globalThis.setInterval=oldInterval;globalThis.clearInterval=oldClear}
});
