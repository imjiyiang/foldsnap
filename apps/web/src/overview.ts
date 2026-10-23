export const overviewSections = [
  { id: "health", title: "备份状态", detail: "最近一次成功备份与当前状态" },
  { id: "progress", title: "当前备份进度", detail: "仅在备份运行或暂停时显示" },
  {
    id: "stats",
    title: "关键数字",
    detail: "计划、文件版本、恢复点与磁盘数量",
  },
  {
    id: "plans",
    title: "计划摘要",
    detail: "最多四个计划的最近结果，管理操作在备份计划页",
  },
  {
    id: "activity",
    title: "最近活动",
    detail: "最近四次运行，完整记录在历史版本页",
  },
  {
    id: "storage",
    title: "磁盘容量摘要",
    detail: "最多两块磁盘的容量，详细读写与清理在磁盘与资源页",
  },
] as const;
export type OverviewSection = (typeof overviewSections)[number]["id"];
export const defaultOverview: OverviewSection[] = [
  "progress",
  "stats",
  "plans",
  "activity",
];
export function readOverview(): OverviewSection[] {
  try {
    const value: unknown = JSON.parse(
      localStorage.getItem("localbackup.overview.v1") || "null",
    );
    if (Array.isArray(value))
      return overviewSections
        .filter((s) => value.includes(s.id))
        .map((s) => s.id);
  } catch {
    /* Storage unavailable: use defaults for this session. */
  }
  return [...defaultOverview];
}
export function readDismissedScans(): string[] {
  try {
    const value: unknown = JSON.parse(
      localStorage.getItem("localbackup.dismissedScans.v1") || "[]",
    );
    return Array.isArray(value)
      ? value.filter((v): v is string => typeof v === "string").slice(-100)
      : [];
  } catch {
    return [];
  }
}
