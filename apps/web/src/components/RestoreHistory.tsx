import { startPolling } from "../polling";
import { useEffect, useState } from "react";
import { api } from "../api";
import { date } from "../types";

type RestoreJob = {
  id: string;
  status: string;
  startedAt: string;
  finishedAt?: string;
  path?: string;
  error?: string;
  request?: {
    snapshotId: string;
    sourceId: string;
    path: string;
    destination: string;
  };
};
const labels: Record<string, string> = {
  running: "恢复进行中",
  success: "恢复成功",
  failed: "恢复失败",
  cancelled: "恢复已取消",
  interrupted: "恢复已中断",
};
export default function RestoreHistory() {
  const [jobs, setJobs] = useState<RestoreJob[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let alive = true,
      inFlight = false;
    const load = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const data = await api<RestoreJob[]>("restores");
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
    const stopPolling = startPolling(load, 2000);
    return () => {
      alive = false;
      stopPolling();
    };
  }, []);
  return (
    <section className="restore-history">
      <p className="muted">
        最近 50
        次文件恢复。恢复始终写入新目录；中断或失败的输出可能不完整，请检查后从历史版本重新恢复。
      </p>
      {error && <div className="notice error">{error}</div>}
      {loading ? (
        <p>正在读取恢复记录…</p>
      ) : !jobs.length ? (
        <div className="queue-empty">
          <h3>还没有恢复记录</h3>
          <p>在历史版本中选择恢复点，浏览并恢复需要的文件。</p>
        </div>
      ) : (
        jobs.map((job) => (
          <article className="settings-box restore-record" key={job.id}>
            <div className="restore-record-heading">
              <strong>{labels[job.status] || job.status}</strong>
              <time>{date(job.startedAt)}</time>
            </div>
            <p>恢复内容：{job.request?.path || "整个源目录"}</p>
            {job.path && (
              <p>
                输出位置：<code>{job.path}</code>
              </p>
            )}
            {job.error && (
              <div className="notice error">
                {job.status === "cancelled"
                  ? "恢复请求已中断，已有输出可能不完整，请检查后重新恢复。"
                  : job.error}
              </div>
            )}
            <small className="muted">
              任务 ID：{job.id}
              {job.finishedAt ? ` · 结束于 ${date(job.finishedAt)}` : ""}
            </small>
          </article>
        ))
      )}
    </section>
  );
}
