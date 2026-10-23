import { startPolling } from "../polling";
import { runLabels as labels } from "../status";
import { useEffect, useState } from "react";
import { Clock, Pause, Play, X, ArrowUpRight, RefreshCw } from "lucide-react";
import { api } from "../api";
import { date, type Run } from "../types";
export default function TaskQueue({
  onSelect,
  notify,
  activeTask,
}: {
  activeTask: string;
  onSelect: (r: Run) => void;
  notify: (message: string, error?: boolean) => void;
}) {
  const [jobs, setJobs] = useState<Run[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState("");
  useEffect(() => {
    let alive = true,
      inFlight = false;
    const load = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const data = await api<Run[]>("system/queue");
        if (alive) {
          setJobs(data);
          setError("");
        }
      } catch (e) {
        if (alive) setError((e as Error).message);
      } finally {
        inFlight = false;
        if (alive) setLoading(false);
      }
    };
    const stopPolling = startPolling(load, 1000);
    return () => {
      alive = false;
      stopPolling();
    };
  }, []);
  const control = async (r: Run, action: string) => {
    setBusy(r.id);
    try {
      await api(`runs/${r.id}/${action}`, "POST", {});
      setJobs(await api<Run[]>("system/queue"));
      notify(
        action === "pause"
          ? "已请求暂停"
          : action === "resume"
            ? "任务已继续"
            : "已请求取消",
      );
    } catch (e) {
      notify((e as Error).message, true);
    } finally {
      setBusy("");
    }
  };
  return (
    <section className="task-queue">
      <div className="notice compact">
        <Clock size={18} />
        <span>
          备份依次执行。暂停正在运行的备份会阻塞后续备份；排队中或等待磁盘的任务不阻塞其他任务。
        </span>
      </div>
      {["index", "restore", "recovery", "retention"].includes(activeTask) && (
        <div className="notice compact">
          <RefreshCw size={16} className="spin" />
          {activeTask === "index"
            ? "正在完整重扫文件索引"
            : activeTask === "restore"
              ? "正在恢复文件"
              : activeTask === "recovery"
                ? "正在重建快照记录"
                : "正在整理历史版本"}
          ，完成后将继续执行备份队列。
        </div>
      )}
      {error && <div className="notice error">{error}</div>}
      {loading ? (
        <p className="muted">正在读取任务队列…</p>
      ) : !jobs.length ? (
        <div className="queue-empty">
          <Clock size={32} />
          <h3>目前没有待执行任务</h3>
          <p>预览后开始备份，忙碌时会自动加入队列。</p>
        </div>
      ) : (
        <div className="queue-list">
          {jobs.map((r, index) => (
            <article className="queue-card" key={r.id}>
              <div className="queue-card-top">
                <span className="queue-index">{index + 1}</span>
                <div>
                  <h3>{r.planName}</h3>
                  <span className={"queue-status " + r.status}>
                    {labels[r.status] ?? r.status}
                  </span>
                </div>
                <button
                  className="icon-btn"
                  aria-label={`查看 ${r.planName} 运行详情`}
                  onClick={() => onSelect(r)}
                >
                  <ArrowUpRight size={18} />
                </button>
              </div>
              <p>{r.stage}</p>
              {r.sourceIds?.length ? (
                <p className="scope-history">
                  仅更新：{(r.sourceNames || r.sourceIds).join("、")}
                </p>
              ) : null}
              {["running", "paused", "pausing"].includes(r.status) && (
                <div className="progress-track">
                  <span style={{ width: r.progress + "%" }} />
                </div>
              )}
              <div className="queue-card-bottom">
                <span>加入队列 {date(r.queuedAt ?? r.startedAt)}</span>
                <div>
                  {r.status === "paused" ? (
                    <button
                      className="btn secondary small"
                      disabled={busy === r.id}
                      onClick={() => void control(r, "resume")}
                    >
                      <Play size={14} />
                      继续
                    </button>
                  ) : (
                    r.status !== "pausing" && (
                      <button
                        className="btn secondary small"
                        disabled={busy === r.id}
                        onClick={() => void control(r, "pause")}
                      >
                        <Pause size={14} />
                        暂停
                      </button>
                    )
                  )}
                  <button
                    className="btn small"
                    disabled={busy === r.id}
                    onClick={() => void control(r, "cancel")}
                  >
                    {busy === r.id ? (
                      <RefreshCw className="spin" size={14} />
                    ) : (
                      <X size={14} />
                    )}
                    取消
                  </button>
                </div>
              </div>
            </article>
          ))}
        </div>
      )}
      <p className="muted filter-help">
        磁盘重连后会重新检查卷身份与预览有效期。服务重启会保留排队和排队暂停任务；被中断的运行标为可恢复，不会自动提交未完成快照。
      </p>
    </section>
  );
}
