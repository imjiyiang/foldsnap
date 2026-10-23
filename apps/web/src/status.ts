import type { Plan } from "./types";

export const runLabels: Record<string, string> = {
  success: "备份成功",
  queued: "排队中",
  waiting_for_volume: "等待目标磁盘",
  waiting_for_source: "等待源目录",
  paused: "本次备份已暂停",
  pausing: "正在暂停本次备份",
  running: "正在备份",
  failed: "备份失败",
  cancelled: "本次备份已取消",
  recoverable: "运行中断，可重试",
  needs_confirmation: "等待重新确认",
};

export function scheduleLabel(
  plan: Pick<Plan, "enabled" | "schedule">,
): string {
  if (!plan.enabled) return "未开启（仅手动）";
  const parts: string[] = [];
  const s = plan.schedule;
  if (s.onChange) parts.push("文件变化后");
  if (s.onMount) parts.push("磁盘连接时");
  if (s.intervalMinutes > 0) {
    parts.push(
      s.intervalMinutes === 1440
        ? "每天"
        : s.intervalMinutes % 1440 === 0
          ? `每 ${s.intervalMinutes / 1440} 天`
          : `每 ${s.intervalMinutes} 分钟`,
    );
  }
  return parts.length ? parts.join(" · ") : "未设置触发条件";
}

export function lastRunLabel(status?: string): string {
  if (!status) return "尚未执行备份";
  return runLabels[status] ?? "未知运行状态";
}
