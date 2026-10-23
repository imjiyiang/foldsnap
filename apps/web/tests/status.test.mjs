import test from 'node:test';
import assert from 'node:assert/strict';
import { scheduleLabel, lastRunLabel } from '../src/status.ts';
test('completed result never becomes paused when automatic backups are disabled', () => {
  assert.equal(scheduleLabel({enabled:false,schedule:{}}), '未开启（仅手动）');
  assert.equal(lastRunLabel('success'), '备份成功');
});
test('every unfinished or unsuccessful run keeps its own meaning', () => {
  for (const status of ['queued','waiting_for_volume','waiting_for_source','paused','pausing','running','failed','cancelled','recoverable','needs_confirmation']) {
    assert.notEqual(lastRunLabel(status), '未知运行状态');
    assert.doesNotMatch(lastRunLabel(status), /成功|准备就绪/);
  }
  assert.equal(lastRunLabel(), '尚未执行备份');
});
test('all enabled triggers are visible and weekly schedules are not called daily', () => {
  assert.equal(scheduleLabel({enabled:true,schedule:{onChange:true,onMount:true,intervalMinutes:10080}}), '文件变化后 · 磁盘连接时 · 每 7 天');
  assert.equal(scheduleLabel({enabled:true,schedule:{intervalMinutes:0}}), '未设置触发条件');
});
