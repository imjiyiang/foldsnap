import { startPolling } from "../polling";
import { useEffect, useState } from "react";
import { Clock } from "lucide-react";
import { api } from "../api";
import { bytes } from "../types";

type Status = {
  decision: string;
  checkedAt?: string;
  evaluatedAt?: string;
  lastMaxCheck?: string;
  failures: number;
  maxAttempts: number;
  retryPending: boolean;
  nextRetryAt?: string;
  changes?: { changed: boolean; files: number; bytes: number };
};
const when = (value?: string) =>
  value ? new Date(value).toLocaleString() : "尚未检查";
export function ScheduleStatus({ planId }: { planId: string }) {
  const [state, setState] = useState<Status | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    setState(null);
    setError("");
    if (!planId) return;
    let current = true;
    const refresh = () => {
      return api<Status>(`plans/${planId}/schedule`)
        .then((value) => {
          if (current) {
            setState(value);
            setError("");
          }
        })
        .catch((e) => {
          if (current) setError((e as Error).message);
        });
    };
    const stopPolling = startPolling(refresh, 5000);
    return () => {
      current = false;
      stopPolling();
    };
  }, [planId]);
  return (
    <div className="schedule-status">
      <h4>
        <Clock size={16} />
        调度状态
      </h4>
      {!planId ? (
        <p>保存计划后可查看实际触发判断。</p>
      ) : error ? (
        <p>{error}</p>
      ) : !state ? (
        <p>正在读取…</p>
      ) : (
        <>
          <b>{state.decision}</b>
          <p>
            {state.changes
              ? `最近检查发现 ${state.changes.files} 个变化路径 · ${bytes(state.changes.bytes)} 逻辑变化`
              : "尚无完整变化统计"}
          </p>
          <small>
            变化检查：{when(state.checkedAt)}
            <br />
            最长间隔检查：{when(state.lastMaxCheck)}
            <br />
            本轮连续失败 {state.failures} 次 · 最多尝试 {state.maxAttempts} 次
            {state.nextRetryAt ? (
              <>
                <br />
                重试不早于：{when(state.nextRetryAt)}
              </>
            ) : null}
          </small>
        </>
      )}
      <p className="muted">
        显示已保存计划的状态。修改后请先保存；睡眠、离线和任务排队会延后执行。
      </p>
    </div>
  );
}
