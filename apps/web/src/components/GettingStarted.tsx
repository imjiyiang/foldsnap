import Select from "./Select";
import { useEffect, useState } from "react";
import {
  CheckCircle2,
  Circle,
  RefreshCw,
  ShieldCheck,
  AlertCircle,
} from "lucide-react";
import { api } from "../api";
import type { Plan } from "../types";

type Item = {
  id: string;
  name: string;
  path: string;
  state: string;
  message: string;
};
type ReadyPlan = {
  id: string;
  name: string;
  enabled: boolean;
  ready: boolean;
  snapshots: number;
  sources: Item[];
  destinations: Item[];
};
type Report = {
  checkedAt: string;
  platform: string;
  databaseReady: boolean;
  rsync: { available: boolean; modern: boolean; version: string };
  plans: ReadyPlan[];
  snapshots: number;
};
export function GettingStarted({
  plans,
  onCreate,
  onEdit,
  onPreview,
  onHistory,
  onSettings,
  onClose,
}: {
  plans: Plan[];
  onCreate: () => void;
  onEdit: (p: Plan) => void;
  onPreview: (p: Plan) => void;
  onHistory: (id: string) => void;
  onSettings: () => void;
  onClose: () => void;
}) {
  const [report, setReport] = useState<Report | null>(null),
    [busy, setBusy] = useState(true),
    [error, setError] = useState(""),
    [selected, setSelected] = useState("");
  const refresh = async () => {
    setBusy(true);
    setError("");
    try {
      setReport(await api<Report>("system/setup"));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    void refresh();
  }, []);
  const readiness =
    report?.plans.find((p) => p.id === selected) || report?.plans[0];
  const plan = plans.find((p) => p.id === readiness?.id);
  const environment =
    !!report?.databaseReady &&
    !!report?.rsync.available &&
    report.platform === "darwin";
  const existing = !!readiness?.snapshots || (!plan && !!report?.snapshots);
  return (
    <div
      className="modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <section
        className="modal setup-guide"
        role="dialog"
        aria-modal="true"
        aria-label="首次备份引导"
      >
        <div className="modal-header">
          <div>
            <h2>开始你的第一份备份</h2>
            <p>选择重要目录，绑定备份磁盘，先预览再开始。</p>
          </div>
          <button
            className="icon-btn"
            onClick={onClose}
            aria-label="关闭首次备份引导"
          >
            ×
          </button>
        </div>
        <div className="setup-body">
          {error && (
            <div className="notice error">
              <AlertCircle size={17} />
              {error}
            </div>
          )}
          <div className="setup-step">
            <span>{environment ? <CheckCircle2 /> : <Circle />}</span>
            <div>
              <h3>1. 检查本地环境</h3>
              <p>
                {busy
                  ? "正在检查环境、目录与磁盘…"
                  : report?.rsync.available
                    ? `备份引擎可用 · ${report.rsync.modern ? "完整扩展属性模式" : "基础元数据模式"}`
                    : "未找到可用备份引擎，请在设置中选择 rsync"}
              </p>
              {report && !report.rsync.modern && (
                <small>
                  基础模式备份文件内容、权限、修改时间和链接；完整扩展属性需
                  rsync 3.x。
                </small>
              )}
              {report && report.platform !== "darwin" && (
                <p>当前平台为 {report.platform}，macOS 原生能力不可用。</p>
              )}
              {!busy && !environment && (
                <button className="text-btn" onClick={onSettings}>
                  打开引擎设置
                </button>
              )}
            </div>
          </div>
          <div className="setup-step">
            <span>{readiness?.ready ? <CheckCircle2 /> : <Circle />}</span>
            <div>
              <h3>2. 选择目录与备份磁盘</h3>
              <p>
                从快照模式开始。首次计划默认关闭自动备份，保存后会在后台准备预览。
              </p>
              {report?.plans.length ? (
                <>
                  <Select
                    aria-label="引导检查的计划"
                    value={readiness?.id ?? report.plans[0].id}
                    onChange={(e) => setSelected(e.target.value)}
                  >
                    {report.plans.map((p) => (
                      <option value={p.id} key={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </Select>
                  <div className="setup-paths">
                    {[
                      ...(readiness?.sources || []),
                      ...(readiness?.destinations || []),
                    ].map((item) => (
                      <div
                        key={item.id}
                        className={item.state === "ready" ? "" : "problem"}
                      >
                        <b>{item.name}</b>
                        <small>{item.path}</small>
                        <p>{item.message}</p>
                      </div>
                    ))}
                  </div>
                  {plan && (
                    <button className="text-btn" onClick={() => onEdit(plan)}>
                      检查或修改这个计划
                    </button>
                  )}
                </>
              ) : (
                <button
                  className="btn primary"
                  disabled={busy || !environment}
                  onClick={onCreate}
                >
                  创建首个计划
                </button>
              )}
              {!!report?.plans.length && (
                <button className="text-btn" onClick={onCreate}>
                  新建另一个计划
                </button>
              )}
            </div>
          </div>
          <div className="setup-step">
            <span>{existing ? <CheckCircle2 /> : <Circle />}</span>
            <div>
              <h3>3. 预览，再生成恢复点</h3>
              <p>
                {existing
                  ? `已有 ${readiness?.snapshots || report?.snapshots || 0} 个恢复点记录。可以浏览历史，或预览下一次变化。`
                  : "检查将新增、修改与移除的文件后，再点击开始备份。首次备份完成后可浏览文件并恢复到新目录。"}
              </p>
              <div className="setup-actions">
                <button
                  className="btn primary"
                  disabled={!environment || !readiness?.ready || !plan || busy}
                  onClick={() => plan && onPreview(plan)}
                >
                  预览备份
                </button>
                {existing && (
                  <button
                    className="btn secondary"
                    onClick={() => onHistory(readiness?.id || "")}
                  >
                    查看历史与恢复
                  </button>
                )}
              </div>
            </div>
          </div>
          <div className="notice">
            <ShieldCheck size={18} />
            <span>
              目录权限属于预检查，实际读写以预览和备份结果为准。若 macOS
              拒绝访问，在系统设置的隐私与安全性中检查 Foldsnap
              的文件访问权限，再重新检查。
            </span>
          </div>
          <p className="muted setup-help">
            确认首份恢复点后，到计划的「调度与保留」启用自动备份。服务需保持运行；插盘触发按已绑定卷
            UUID 识别。
          </p>
        </div>
        <div className="modal-footer">
          <small className="muted">
            {report
              ? `检查于 ${new Date(report.checkedAt).toLocaleTimeString()}`
              : "等待环境检查"}
          </small>
          <button
            className="btn secondary"
            onClick={() => void refresh()}
            disabled={busy}
          >
            <RefreshCw size={15} className={busy ? "spin" : ""} />
            重新检查
          </button>
          <button className="btn secondary" onClick={onClose}>
            稍后设置
          </button>
        </div>
      </section>
    </div>
  );
}
