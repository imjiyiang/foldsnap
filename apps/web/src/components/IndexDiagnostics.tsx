import { startPolling } from "../polling";
import Select from "./Select";
import { useEffect, useState } from "react";
import { Database, RefreshCw, ShieldCheck } from "lucide-react";
import { api } from "../api";
import { date, type Plan, type ScanStats } from "../types";
import FingerprintStats from "./FingerprintStats";
type IndexStatus = {
  cacheAvailable: boolean;
  cacheError?: string;
  paths: number;
  objects: number;
  selected: number;
  excluded: number;
  sources: {
    sourceId: string;
    root: string;
    volumeId: string;
    filesystem: string;
    cacheable: boolean;
    generation: number;
    completedAt: string;
    scanMode: string;
    walkedPaths: number;
  }[];
};
export default function IndexDiagnostics({
  plans,
  active,
  notify,
}: {
  plans: Plan[];
  active: boolean;
  notify: (s: string, error?: boolean) => void;
}) {
  const [value, setValue] = useState<IndexStatus | null>(null),
    [planId, setPlanId] = useState(plans[0]?.id ?? ""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [stats, setStats] = useState<ScanStats>();
  useEffect(() => {
    let alive = true;
    const load = () =>
      api<IndexStatus>("system/index")
        .then((v) => {
          if (alive) {
            setValue(v);
            setError("");
          }
        })
        .catch((e) => {
          if (alive) setError(e.message);
        });
    const stopPolling = startPolling(load, 10000);
    return () => {
      alive = false;
      stopPolling();
    };
  }, []);
  const names = new Map(
    plans.flatMap((p) => p.sources.map((s) => [s.id, s.name] as const)),
  );
  const scan = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await api<{ scanStats: ScanStats }>(
        "system/index/scan",
        "POST",
        { planId },
      );
      setStats(result.scanStats);
      setValue(await api<IndexStatus>("system/index"));
      notify("已完整重读源文件并更新索引");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="settings-box index-diagnostics">
      <div className="settings-title">
        <span className="setting-icon">
          <Database size={22} />
        </span>
        <div>
          <h3>文件索引与内容指纹</h3>
          <p>按物理文件身份复用已知指纹，减少重复读取。</p>
        </div>
      </div>
      {(error || value?.cacheError) && (
        <div className="notice error">{error || value?.cacheError}</div>
      )}
      <div className="index-counts">
        <div>
          <span>已索引路径</span>
          <b>{value?.paths.toLocaleString() ?? "—"}</b>
        </div>
        <div>
          <span>指纹版本</span>
          <b>{value?.objects.toLocaleString() ?? "—"}</b>
        </div>
        <div>
          <span>规则排除项</span>
          <b>{value?.excluded.toLocaleString() ?? "—"}</b>
        </div>
      </div>
      <div className="index-sources">
        {value?.sources.map((s) => (
          <div key={s.sourceId}>
            <div>
              <b>{names.get(s.sourceId) ?? "已索引目录"}</b>
              <span title={s.root}>{s.root}</span>
            </div>
            <div>
              <b>
                {s.cacheable ? "指纹缓存可用" : "完整读取"} · {s.filesystem}
              </b>
              <span>
                {s.scanMode === "subtree" ? "变化子树" : "完整扫描"} · 访问{" "}
                {s.walkedPaths.toLocaleString()} 个路径 · {date(s.completedAt)}
              </span>
            </div>
          </div>
        ))}
      </div>
      <p className="muted filter-help">
        缓存按卷身份、文件
        ID、大小、mtime、ctime、创建时间与属性匹配。当前对有可靠时间戳的 APFS
        启用持久化复用。原生事件与索引校验通过时只访问变化子树，丢事件、规则变化或每小时审计会回退完整扫描；其他文件系统保留完整读取。
      </p>
      <div className="index-rescan">
        <Select
          aria-label="完整重扫的备份计划"
          value={planId}
          onChange={(e) => {
            setPlanId(e.target.value);
            setStats(undefined);
          }}
        >
          {plans.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
        <button
          className="btn secondary"
          disabled={!planId || busy || active}
          onClick={() => void scan()}
        >
          <RefreshCw size={15} className={busy ? "spin" : ""} />
          {busy ? "正在完整重扫…" : "完整重扫源文件"}
        </button>
      </div>
      <FingerprintStats value={stats} />
      <div className="notice compact">
        <ShieldCheck size={16} />
        重扫只更新索引。快照提交前的完整内容校验、事件丢失审计及旧版本恢复不依赖缓存。
      </div>
    </div>
  );
}
