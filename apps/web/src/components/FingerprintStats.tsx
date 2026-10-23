import { bytes, type ScanStats } from "../types";
import { Database } from "lucide-react";
export default function FingerprintStats({ value }: { value?: ScanStats }) {
  if (!value) return null;
  return (
    <div className="fingerprint-stats">
      <Database size={15} />
      <span>
        {value.forced
          ? "完整重读"
          : value.subtreeSources
            ? "按变化更新索引"
            : "本次源目录扫描"}
        {" · "}实际访问 <b>{value.pathsVisited ?? "—"}</b> 个路径，沿用{" "}
        <b>{value.projectedPaths ?? 0}</b> 个未变化路径
        {" · "}指纹读取 <b>{value.filesHashed}</b> 个文件（
        {bytes(value.bytesHashed)}），复用 <b>{value.cacheHits}</b> 个指纹（
        {bytes(value.bytesReused)}）
      </span>
    </div>
  );
}
