import { startPolling } from "../polling";
import { useEffect, useState } from "react";
import { Activity, ArrowUpRight, HardDrive, AlertCircle } from "lucide-react";
import { api } from "../api";
import { bytes, type Run } from "../types";

type Work = {
  cachedFiles?: number;
  cachedBytes?: number;
  runId: string;
  phase: string;
  path: string;
  source: string;
  destination: string;
  bytes: number;
  files: number;
  bytesPerSecond: number;
  filePercent?: number;
  updatedAt: string;
};
type Disk = {
  id: string;
  name: string;
  device: string;
  location: string;
  readBps: number | null;
  writeBps: number | null;
};
type Sample = { at: string; disks: Disk[]; work: Work };
type Monitor = {
  samples: Sample[] | null;
  error: string;
  intervalSeconds: number;
};
const phases: Record<string, string> = {
  scan: "扫描与内容指纹",
  prepare: "准备与复用已有文件",
  transfer: "文件传输",
  verify: "快照内容校验",
  preflight: "传输预检查",
  verify_baseline: "已有恢复点完整校验",
  verify_source: "源文件完整校验",
  verify_closing: "结束复核",
  done: "运行结束",
};
const rate = (v: number | null | undefined) =>
  v == null ? "—" : `${bytes(v)}/s`;
function Chart({ samples, id }: { samples: Sample[]; id: string }) {
  const values = samples
    .slice(-60)
    .map((s) => s.disks.find((d) => d.id === id));
  const max = Math.max(
    1024,
    ...values.flatMap((d) => [d?.readBps ?? 0, d?.writeBps ?? 0]),
  );
  const points = (key: "readBps" | "writeBps") => {
    let connected = false;
    return values
      .map((d, i) => {
        if (d?.[key] == null) {
          connected = false;
          return "";
        }
        const command = connected ? "L" : "M";
        connected = true;
        return `${command}${600 - (values.length - 1 - i) * 10},${100 - ((d[key] ?? 0) / max) * 86}`;
      })
      .join(" ");
  };
  return (
    <div className="io-chart">
      <span>{rate(max)}</span>
      <svg viewBox="0 0 600 110" role="img" aria-label="最近 60 秒磁盘读写速度">
        <path d="M0 20H600 M0 60H600 M0 100H600" className="chart-grid" />
        <path d={points("readBps")} className="chart-read" />
        <path d={points("writeBps")} className="chart-write" />
      </svg>
      <div>
        <span>60 秒前</span>
        <span>现在</span>
      </div>
    </div>
  );
}
export default function ResourceMonitor({
  runs,
  compact = false,
  onOpen,
}: {
  runs: Run[];
  compact?: boolean;
  onOpen?: () => void;
}) {
  const [data, setData] = useState<Monitor | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    let alive = true,
      busy = false;
    const load = async () => {
      if (busy) return;
      busy = true;
      try {
        const d = await api<Monitor>("system/monitor");
        if (alive) {
          setData(d);
          setError("");
        }
      } catch (e) {
        if (alive) setError((e as Error).message);
      } finally {
        busy = false;
      }
    };
    const stopPolling = startPolling(load, 1000);
    return () => {
      alive = false;
      stopPolling();
    };
  }, []);
  const samples = data?.samples ?? [],
    sample = samples.at(-1),
    work = sample?.work;
  const run = runs.find((r) => r.id === work?.runId),
    active = !!run && ["running", "paused", "pausing"].includes(run.status);
  const stale = sample && Date.now() - new Date(sample.at).getTime() > 5000;
  const speed = active
    ? run?.status === "paused"
      ? 0
      : error || stale
        ? null
        : work?.bytesPerSecond
    : 0;
  const disks = sample?.disks ?? [];
  if (compact && !active) {
    if (!onOpen) return null;
    return (
      <button className="monitor-entry" onClick={onOpen}>
        <Activity size={18} />
        资源监控<span>查看内外置磁盘实时读写</span>
        <ArrowUpRight size={17} />
      </button>
    );
  }
  return (
    <section
      className={compact ? "resource-monitor compact" : "resource-monitor"}
    >
      <div className="monitor-heading">
        <div>
          <span className={"live-dot " + (error || stale ? "offline" : "")} />
          <b>
            {compact
              ? (run?.status === "paused"
                  ? "已暂停 · "
                  : run?.status === "pausing"
                    ? "正在暂停 · "
                    : "正在备份 · ") + run?.planName
              : "实时资源监控"}
          </b>
        </div>
        {compact && onOpen ? (
          <button className="btn" onClick={onOpen}>
            查看监控 <ArrowUpRight size={15} />
          </button>
        ) : (
          <span>每秒采样 · 最近 2 分钟</span>
        )}
      </div>
      {(error || data?.error || stale) && (
        <div className="notice error">
          <AlertCircle size={16} />
          {error || data?.error || "采样已过期，正在重新连接"}
        </div>
      )}
      <div className="monitor-work">
        <div className="monitor-run">
          <span className="eyebrow">{active ? "当前备份" : "备份引擎"}</span>
          <h3>{active ? run.planName : "当前没有备份任务"}</h3>
          <p>
            {active
              ? run.status === "paused"
                ? "备份已暂停 · 继续后接着处理"
                : run.status === "pausing"
                  ? "正在等待安全检查点"
                  : (phases[work?.phase ?? ""] ?? run.stage)
              : "磁盘监控持续运行，开始备份后显示处理进度。"}
          </p>
          {active && (
            <>
              <div className="progress-track">
                <span style={{ width: run.progress + "%" }} />
              </div>
              <small>
                任务流程 {run.progress}% · {run.stage}
              </small>
            </>
          )}
        </div>
        <div className="monitor-number">
          <span>
            {work?.phase === "transfer" ? "文件传输速度" : "内容处理速度"}
          </span>
          <strong>{rate(speed)}</strong>
          <small>{active ? "当前阶段 · 逻辑数据量" : "等待备份任务"}</small>
        </div>
        <div className="monitor-number">
          <span>当前阶段已处理</span>
          <strong>{active ? bytes(work?.bytes ?? 0) : "—"}</strong>
          <small>
            {active && work?.phase === "transfer"
              ? `${work.files} 个文件已传输`
              : work?.phase === "scan" && work.cachedFiles
                ? `${work.cachedFiles} 个指纹复用 · ${bytes(work.cachedBytes ?? 0)}`
                : "扫描、传输和校验分别计数"}
          </small>
        </div>
      </div>
      {active && (
        <div className="monitor-path">
          <Activity size={16} />
          <div>
            <b>{work?.source && `${work.source} → ${work.destination}`}</b>
            <span>{work?.path || "正在处理所选文件"}</span>
          </div>
          {work?.phase === "transfer" && work.filePercent != null && (
            <span className="file-percent">当前文件 {work.filePercent}%</span>
          )}
        </div>
      )}
      {!compact && (
        <>
          <div className="monitor-disk-heading">
            <h3>磁盘读写</h3>
            <span>
              <i className="read-key" />
              读取 <i className="write-key" />
              写入
            </span>
          </div>
          <p className="muted">
            物理设备的总吞吐量，包含其他应用的读写。APFS
            共享卷合并显示；缓存命中时，文件处理速度可高于实际磁盘速度。
          </p>
          <div className="monitor-disks">
            {disks.map((d) => (
              <article className="monitor-disk" key={d.id}>
                <div className="disk-title">
                  <HardDrive size={22} />
                  <div>
                    <h3>{d.name}</h3>
                    <span>
                      {d.location === "internal"
                        ? "内置磁盘"
                        : d.location === "external"
                          ? "外置磁盘"
                          : "设备位置未知"}{" "}
                      · /dev/{d.device}
                    </span>
                  </div>
                </div>
                <div className="disk-rates">
                  <div>
                    <span>读取</span>
                    <strong>{rate(error || stale ? null : d.readBps)}</strong>
                  </div>
                  <div>
                    <span>写入</span>
                    <strong>{rate(error || stale ? null : d.writeBps)}</strong>
                  </div>
                </div>
                <Chart samples={samples} id={d.id} />
              </article>
            ))}
          </div>
          {!disks.length && !data?.error && (
            <div className="empty-state">
              {data
                ? "没有可读取计数器的物理磁盘。磁盘映像及部分设备驱动可能不提供此数据。"
                : "正在读取磁盘计数器…"}
            </div>
          )}
          <p className="muted monitor-footnote">
            “—”表示采样尚未就绪或不可用。文件传输进度不包含后续校验与原子提交，只有完整校验通过后任务才会成功。
          </p>
        </>
      )}
    </section>
  );
}
