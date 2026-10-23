import { version as interfaceVersion } from "../package.json";
import { startPolling } from "./polling";
import RestoreHistory from "./components/RestoreHistory";
import {
  overviewSections,
  defaultOverview,
  readOverview,
  readDismissedScans,
  type OverviewSection,
} from "./overview";
import Select from "./components/Select";
import { runLabels as labels, scheduleLabel, lastRunLabel } from "./status";
import { GettingStarted } from "./components/GettingStarted";
import { ScheduleStatus } from "./components/ScheduleStatus";
import { SourceBinding } from "./components/SourceBinding";
import React, { useState, useEffect, useCallback, useRef } from "react";
import { createRoot } from "react-dom/client";
import {
  ArrowUpRight,
  ArrowRight,
  ArrowLeft,
  Plus,
  LayoutDashboard,
  Layers,
  History,
  HardDrive,
  Settings,
  ChevronRight,
  ChevronDown,
  Folder,
  FolderOpen,
  MoreHorizontal,
  Play,
  Pause,
  Check,
  CheckCheck,
  X,
  Search,
  ShieldCheck,
  RefreshCw,
  Clock,
  ArrowDownToLine,
  Activity,
  CheckCircle2,
  AlertCircle,
  LoaderCircle,
  ExternalLink,
  File,
  Trash2,
  Pin,
  Unplug,
  Command,
  SlidersHorizontal,
  PanelLeftClose,
  Terminal,
  Copy,
  MoveRight,
  FileCheck2,
  WifiOff,
  LockKeyhole,
  ListOrdered,
} from "lucide-react";
import { api, ApiError, hasToken, setToken } from "./api";
import type {
  Plan,
  PreviewJob,
  Run,
  Snapshot,
  Volume,
  Status,
  Preview,
  Source,
  Destination,
  Entry,
  FileFilters,
} from "./types";
import { bytes, date } from "./types";
import ResourceMonitor from "./components/ResourceMonitor";
import IndexDiagnostics from "./components/IndexDiagnostics";
import FingerprintStats from "./components/FingerprintStats";
import TaskQueue from "./components/TaskQueue";
import FileFiltersEditor from "./components/FileFiltersEditor";
import RuleResults, { type RulePreview } from "./components/RuleResults";
import PreviewOperations from "./components/PreviewOperations";
import "./style.css";

type Page =
  "dashboard" | "plans" | "history" | "storage" | "settings" | "queue";
const titles: Record<Page, string> = {
  queue: "任务中心",
  dashboard: "概览",
  plans: "备份计划",
  history: "历史版本",
  storage: "磁盘与资源",
  settings: "设置",
};
const nav = [
  { id: "dashboard", icon: LayoutDashboard },
  { id: "plans", icon: Layers },
  { id: "queue", icon: ListOrdered },
  { id: "history", icon: History },
  { id: "storage", icon: HardDrive },
] as const;
const operationLabels: Record<string, string> = {
  added: "新增",
  modified: "修改",
  moved: "移动",
  deleted: "移除",
  ignored: "忽略",
  retained: "保留",
  replaced: "类型变化",
};
const modeLabels: Record<string, string> = {
  snapshot: "快照备份",
  incremental: "增量同步",
  mirror: "镜像同步",
};
const emptyPlan = (): Plan => ({
  id: "",
  name: "",
  description: "",
  mode: "snapshot",
  enabled: false,
  sources: [],
  destinations: [],
  rules: [
    { action: "exclude", pattern: ".DS_Store" },
    { action: "exclude", pattern: "**/node_modules/**" },
    { action: "exclude", pattern: "**/.Trash/**" },
  ],
  schedule: {
    intervalMinutes: 1440,
    onMount: true,
    onChange: true,
    debounceSeconds: 120,
    minChangedFiles: 10,
    minChangedBytes: 0,
    minIntervalMinutes: 60,
    maxIntervalMinutes: 1440,
    maxAttempts: 3,
  },
  retention: { keepLast: 10, keepDays: 30 },
});
function navigateTabs(event: React.KeyboardEvent<HTMLDivElement>) {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  const tabs = Array.from(
    event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'),
  );
  const current = tabs.findIndex(
    (tab) => tab.getAttribute("aria-selected") === "true",
  );
  if (!tabs.length || current < 0) return;
  event.preventDefault();
  const next =
    event.key === "Home"
      ? 0
      : event.key === "End"
        ? tabs.length - 1
        : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) %
          tabs.length;
  tabs[next].click();
  tabs[next].focus();
}
function App() {
  const [page, setPage] = useState<Page>("dashboard"),
    [plans, setPlans] = useState<Plan[]>([]),
    [runs, setRuns] = useState<Run[]>([]),
    [scanJobs, setScanJobs] = useState<PreviewJob[]>([]),
    [dismissedScans, setDismissedScans] =
      useState<string[]>(readDismissedScans),
    [snapshots, setSnapshots] = useState<Snapshot[]>([]),
    [volumes, setVolumes] = useState<Volume[]>([]),
    [status, setStatus] = useState<Status | null>(null),
    [loading, setLoading] = useState(true),
    [connected, setConnected] = useState(true),
    [auth, setAuth] = useState(hasToken()),
    [tokenInput, setTokenInput] = useState("");
  const [toast, setToast] = useState<{ text: string; error?: boolean } | null>(
      null,
    ),
    [setupOpen, setSetupOpen] = useState(false),
    [editor, setEditor] = useState<Plan | null>(null),
    [preview, setPreview] = useState<{
      plan: Plan;
      jobId?: string;
      value?: Preview;
      loading: boolean;
      confirmed?: boolean;
      sourceIds?: string[];
    } | null>(null),
    [sourcePicker, setSourcePicker] = useState<{
      plan: Plan;
      ids: string[];
    } | null>(null),
    [runDetail, setRunDetail] = useState<Run | null>(null),
    [fileSnapshot, setFileSnapshot] = useState<Snapshot | null>(null),
    [confirm, setConfirm] = useState<{
      title: string;
      text: string;
      action: () => Promise<void>;
    } | null>(null),
    [historyPlan, setHistoryPlan] = useState(""),
    [filter, setFilter] = useState("all"),
    [query, setQuery] = useState(""),
    [busy, setBusy] = useState(false);
  const [overview, setOverview] = useState<OverviewSection[]>(readOverview);
  const [customizeOverview, setCustomizeOverview] = useState(false);
  const [diskTab, setDiskTab] = useState<"capacity" | "activity" | "retention">(
    "capacity",
  );
  const [taskTab, setTaskTab] = useState<"backups" | "scans" | "restores">(
    "backups",
  );
  useEffect(() => {
    try {
      localStorage.setItem("localbackup.overview.v1", JSON.stringify(overview));
    } catch {}
  }, [overview]);
  useEffect(() => {
    try {
      localStorage.setItem(
        "localbackup.dismissedScans.v1",
        JSON.stringify(dismissedScans.slice(-100)),
      );
    } catch {}
  }, [dismissedScans]);
  const showOverview = (id: OverviewSection) => overview.includes(id);
  const visibleScans = scanJobs
    .filter(
      (job, index, all) =>
        !dismissedScans.includes(job.id) &&
        !(
          job.state === "ready" &&
          job.previewId &&
          runs.some((run) => run.approvedPreviewId === job.previewId)
        ) &&
        (["queued", "running", "cancelling"].includes(job.state) ||
          !all
            .slice(0, index)
            .some(
              (other) =>
                other.planId === job.planId &&
                JSON.stringify(other.sourceIds) ===
                  JSON.stringify(job.sourceIds),
            )),
    )
    .sort((a, b) =>
      Number(!a.finishedAt) < Number(!b.finishedAt)
        ? 1
        : Number(!a.finishedAt) > Number(!b.finishedAt)
          ? -1
          : 0,
    );
  const previewSerial = useRef(0);
  const notify = useCallback(
    (text: string, error = false) => setToast({ text, error }),
    [],
  );
  const refreshBusy = useRef(false);
  const volumeBusy = useRef(false);
  const volumeCheckedAt = useRef(0);
  const refresh = useCallback(async () => {
    if (refreshBusy.current) return;
    if (!hasToken()) {
      setLoading(false);
      return;
    }
    refreshBusy.current = true;
    // Disk discovery is independent: a sleeping disk must not hide the dashboard.
    if (!volumeBusy.current && Date.now() - volumeCheckedAt.current >= 30000) {
      volumeCheckedAt.current = Date.now();
      volumeBusy.current = true;
      void api<Volume[]>("volumes")
        .then(setVolumes)
        .catch(() => {})
        .finally(() => {
          volumeBusy.current = false;
        });
    }
    try {
      const [p, r, s, st, jobs] = await Promise.all([
        api<Plan[]>("plans"),
        api<Run[]>("runs?summary=true"),
        api<Snapshot[]>("snapshots"),
        api<Status>("system/status"),
        api<PreviewJob[]>("preview-jobs"),
      ]);
      setScanJobs(jobs);
      setPlans(p);
      setRuns(r);
      setSnapshots(s);
      setStatus(st);
      setConnected(true);
      setAuth(true);
    } catch (e) {
      setConnected(false);
      if (e instanceof ApiError && e.status === 401) setAuth(false);
      else if (loading) notify((e as Error).message, true);
    } finally {
      refreshBusy.current = false;
      setLoading(false);
    }
  }, [notify, loading]);
  useEffect(() => {
    return startPolling(refresh, 3000);
  }, [refresh]);
  useEffect(() => {
    if (toast) {
      const t = setTimeout(() => setToast(null), 5000);
      return () => clearTimeout(t);
    }
  }, [toast]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setCustomizeOverview(false);
        setEditor(null);
        setSetupOpen(false);
        setPreview(null);
        setSourcePicker(null);
        setRunDetail(null);
        setFileSnapshot(null);
        setConfirm(null);
      }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, []);
  const safe = async (fn: () => Promise<void>) => {
    try {
      await fn();
    } catch (e) {
      notify((e as Error).message, true);
    }
  };
  const showPreview = async (
    plan: Plan,
    sourceIds?: string[],
    background = false,
  ) => {
    const requestKey = "request-" + ++previewSerial.current;
    if (!background)
      setPreview({ plan, sourceIds, loading: true, jobId: requestKey });
    try {
      const job = await api<PreviewJob>(
        `plans/${plan.id}/preview-jobs`,
        "POST",
        { sourceIds },
      );
      if (!background)
        setPreview((old) =>
          old?.jobId === requestKey ? { ...old, jobId: job.id } : old,
        );
      notify("扫描已交给后台，可继续操作；完成后查看预览再开始备份");
      await refresh();
    } catch (e) {
      setPreview((old) => (old?.jobId === requestKey ? null : old));
      notify((e as Error).message, true);
    }
  };
  const openScan = (job: PreviewJob) => {
    const plan = plans.find((p) => p.id === job.planId);
    if (!plan) {
      notify("计划已不存在", true);
      return;
    }
    if (
      ["failed", "cancelled"].includes(job.state) ||
      (job.finishedAt &&
        Date.now() - Date.parse(job.finishedAt) > 9 * 60 * 1000)
    ) {
      void showPreview(plan, job.sourceIds);
    } else
      setPreview({
        plan,
        sourceIds: job.sourceIds,
        loading: true,
        jobId: job.id,
      });
  };
  useEffect(() => {
    if (
      !preview?.loading ||
      !preview.jobId ||
      preview.jobId.startsWith("request-")
    )
      return;
    const id = preview.jobId;
    let stale = false;
    const poll = async () => {
      try {
        const job = await api<PreviewJob>(`preview-jobs/${id}`);
        if (stale) return;
        if (job.state === "ready" && job.result) {
          setPreview((old) =>
            old?.jobId === id
              ? {
                  ...old,
                  value: job.result,
                  sourceIds: job.sourceIds,
                  loading: false,
                }
              : old,
          );
        } else if (["failed", "cancelled"].includes(job.state)) {
          setPreview((old) => (old?.jobId === id ? null : old));
          notify(job.error || job.stage, job.state === "failed");
        }
      } catch (e) {
        if (!stale) notify((e as Error).message, true);
      }
    };
    const stopPolling = startPolling(poll, 1500);
    return () => {
      stale = true;
      stopPolling();
    };
  }, [preview?.jobId, preview?.loading, notify]);
  const controlRun = (id: string, action: "pause" | "resume") =>
    void safe(async () => {
      await api(`runs/${id}/${action}`, "POST", {});
      notify(
        action === "pause"
          ? "已请求暂停本次备份，等待安全检查点"
          : "本次备份已继续",
      );
      await refresh();
    });
  const cancelRun = (id: string) =>
    void safe(async () => {
      await api(`runs/${id}/cancel`, "POST", {});
      notify("已请求取消本次备份，已有恢复点保持完整");
      await refresh();
    });
  const start = async (p: Plan) => {
    setBusy(true);
    try {
      const run = await api<Run>(`plans/${p.id}/runs`, "POST", {
        previewId: preview?.value?.previewId,
        confirmDeletes: !!preview?.confirmed,
        sourceIds: preview?.sourceIds,
      });
      if (preview?.jobId) setDismissedScans((ids) => [...ids, preview.jobId!]);
      setPreview(null);
      setRunDetail(run);
      notify(
        run.status === "queued"
          ? "备份已加入队列"
          : run.status === "paused"
            ? "此计划已有暂停任务，可在任务中心中继续"
            : "备份已开始，完成后可在历史版本中恢复",
      );
      await refresh();
    } catch (e) {
      notify((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };
  const toggle = (p: Plan) =>
    safe(async () => {
      await api(`plans/${p.id}`, "PATCH", { ...p, enabled: !p.enabled });
      await refresh();
      notify(p.enabled ? "自动备份已关闭" : "自动备份已开启");
    });
  const deletePlan = (p: Plan) =>
    setConfirm({
      title: "删除这个备份计划？",
      text: `“${p.name}”的配置将被移除，已有快照与运行记录仍会保留。`,
      action: async () => {
        await api(`plans/${p.id}`, "DELETE");
        await refresh();
        notify("计划已删除，历史快照已保留");
      },
    });
  const latestSuccess = runs.find((r) => r.status === "success"),
    active = runs.find(
      (r) =>
        r.id === status?.activeRunId &&
        ["running", "paused", "pausing"].includes(r.status),
    ),
    totalFiles = snapshots.reduce((a, s) => a + s.files, 0);
  if (!auth)
    return (
      <div className="auth-screen">
        <div className="auth-card">
          <Logo />
          <h1>连接你的本地备份</h1>
          <p>使用 Foldsnap 服务启动时显示的安全链接，或输入访问令牌。</p>
          <input
            aria-label="访问令牌"
            type="password"
            placeholder="粘贴访问令牌"
            value={tokenInput}
            onChange={(e) => setTokenInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                setToken(tokenInput.trim());
                void refresh();
              }
            }}
          />
          <button
            className="btn primary"
            onClick={() => {
              setToken(tokenInput.trim());
              void refresh();
            }}
          >
            安全连接 <ArrowRight size={16} />
          </button>
          <small>
            <LockKeyhole size={13} /> 仅连接 127.0.0.1，数据始终留在本机
          </small>
          {toast && <p className="error-text">{toast.text}</p>}
        </div>
      </div>
    );
  return (
    <div className="app">
      <aside className="sidebar">
        <a
          className="brand"
          href="#"
          onClick={(e) => {
            e.preventDefault();
            setPage("dashboard");
          }}
        >
          <Logo />
          <span>
            Foldsnap<small>留档 · 本地快照备份</small>
          </span>
        </a>
        <div className="workspace">
          <div className="mac-icon">
            <Command size={18} />
          </div>
          <div>
            我的 Mac<small>个人工作空间</small>
          </div>
          <ChevronDown size={14} />
        </div>
        <div className="nav-label">工作空间</div>
        <nav>
          {nav.map(({ id, icon: Icon }) => (
            <button
              key={id}
              className={page === id ? "nav-item active" : "nav-item"}
              onClick={() => {
                if (id === "queue")
                  setTaskTab(visibleScans.length ? "scans" : "backups");
                setPage(id);
              }}
            >
              <Icon size={18} />
              {titles[id]}
              {id === "queue" &&
                visibleScans.filter((j) =>
                  ["queued", "running", "cancelling", "ready"].includes(
                    j.state,
                  ),
                ).length > 0 && (
                  <span className="nav-count">
                    {
                      visibleScans.filter((j) =>
                        ["queued", "running", "cancelling", "ready"].includes(
                          j.state,
                        ),
                      ).length
                    }
                  </span>
                )}
              {id === "plans" && (
                <span className="nav-count">{plans.length}</span>
              )}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="local-note">
            <span className="tiny-shield">
              <ShieldCheck size={19} />
            </span>
            <b>你的数据，只属于你</b>
            <p>
              所有文件保存在本地磁盘，
              <br />
              无需云端，始终由你掌控。
            </p>
          </div>
          <button
            className={"nav-item " + (page === "settings" ? "active" : "")}
            onClick={() => setPage("settings")}
          >
            <Settings size={18} />
            设置
          </button>
          <div className="sidebar-footer">
            <span className={connected ? "dot" : "dot gray"} />
            {connected ? "本地服务运行中" : "服务连接中断"}
            <span>{status?.version ? `v${status.version}` : "正在连接…"}</span>
          </div>
        </div>
      </aside>
      <main>
        <header className="topbar">
          <div>
            <span className="breadcrumb">工作空间</span>
            <ChevronRight size={13} />
            <span>{titles[page]}</span>
          </div>
          {visibleScans.some((j) =>
            ["queued", "running", "cancelling"].includes(j.state),
          ) && (
            <button
              className="text-btn"
              onClick={() => {
                setTaskTab("scans");
                setPage("queue");
              }}
            >
              <LoaderCircle size={14} className="spin" />
              扫描进行中
            </button>
          )}
        </header>
        <div className="content">
          <div className="page-heading">
            <div>
              <h1>{page === "dashboard" ? "备份概览" : titles[page]}</h1>
              <p>
                {page === "dashboard"
                  ? "让备份在后台发生，把时间留给重要的事。"
                  : page === "plans"
                    ? "把重要的目录放进计划，其他的交给 Foldsnap。"
                    : page === "queue"
                      ? "集中管理备份任务、后台扫描与待确认的预览。"
                      : page === "history"
                        ? "每一个时间点，都是一份安心。随时找回需要的文件。"
                        : page === "storage"
                          ? "在同一处查看磁盘容量、实时读写和历史版本清理。"
                          : "按照你的工作方式，调整 Foldsnap。"}
              </p>
            </div>
            {page === "settings" && (
              <button
                className="btn secondary"
                onClick={() => setSetupOpen(true)}
              >
                备份设置引导
              </button>
            )}
            {page === "dashboard" && (
              <button
                className="btn secondary"
                onClick={() => setCustomizeOverview(true)}
              >
                <SlidersHorizontal size={17} />
                自定义概览
              </button>
            )}
            {page === "plans" && (
              <button
                className="btn primary"
                onClick={() => setEditor(emptyPlan())}
              >
                <Plus size={17} />
                新建备份计划
              </button>
            )}
          </div>
          {connected &&
            status?.version &&
            status.version !== interfaceVersion && (
              <div className="notice" role="status">
                <RefreshCw size={17} />
                <span>
                  本地服务版本 {status.version} 与当前页面版本{" "}
                  {interfaceVersion}
                  不一致。请保存正在编辑的内容后重新载入。
                </span>
                <button
                  className="btn secondary"
                  onClick={() => {
                    if (editor) {
                      setConfirm({
                        title: "重新载入页面",
                        text: "当前计划编辑内容尚未保存。重新载入会关闭编辑器，后台任务会继续运行。",
                        action: async () => {
                          window.location.reload();
                        },
                      });
                    } else {
                      window.location.reload();
                    }
                  }}
                >
                  重新载入
                </button>
              </div>
            )}
          {!connected && (
            <div className="notice error">
              <WifiOff size={17} />
              无法连接本地服务。请确认 Foldsnap 正在运行。
              <button onClick={() => void refresh()}>重新连接</button>
            </div>
          )}
          {loading ? (
            <div className="loading-state">
              <LoaderCircle className="spin" />
              正在读取本地备份状态…
            </div>
          ) : (
            <>
              {page === "queue" && (
                <>
                  <div
                    className="section-tabs"
                    onKeyDown={navigateTabs}
                    role="tablist"
                    aria-label="任务分类"
                  >
                    <button
                      role="tab"
                      tabIndex={taskTab === "backups" ? 0 : -1}
                      aria-selected={taskTab === "backups"}
                      onClick={() => setTaskTab("backups")}
                    >
                      备份任务
                    </button>
                    <button
                      role="tab"
                      tabIndex={taskTab === "scans" ? 0 : -1}
                      aria-selected={taskTab === "scans"}
                      onClick={() => setTaskTab("scans")}
                    >
                      扫描与预览 <span>{visibleScans.length}</span>
                    </button>
                    <button
                      role="tab"
                      tabIndex={taskTab === "restores" ? 0 : -1}
                      aria-selected={taskTab === "restores"}
                      onClick={() => setTaskTab("restores")}
                    >
                      恢复记录
                    </button>
                  </div>
                  <section
                    role="tabpanel"
                    aria-label={
                      taskTab === "backups"
                        ? "备份任务"
                        : taskTab === "restores"
                          ? "恢复记录"
                          : "扫描与预览"
                    }
                  >
                    {taskTab === "backups" ? (
                      <TaskQueue
                        onSelect={setRunDetail}
                        notify={notify}
                        activeTask={status?.activeRunId ?? ""}
                      />
                    ) : taskTab === "restores" ? (
                      <RestoreHistory />
                    ) : (
                      <>
                        {visibleScans.length > 0 ? (
                          <section
                            className="background-scans"
                            aria-label="后台扫描任务"
                          >
                            <h3>
                              <Activity size={17} /> 后台扫描{" "}
                              <small>只准备预览，不会自动备份</small>
                            </h3>
                            {visibleScans.map((job) => (
                              <div className="background-scan" key={job.id}>
                                <div>
                                  <b>{job.planName}</b>
                                  <span>
                                    {job.stage}
                                    {job.pathsRead > 0
                                      ? ` · 已检查 ${job.pathsRead.toLocaleString()} 个文件 · 内容读取 ${bytes(job.bytesRead)}`
                                      : ""}
                                  </span>
                                  {job.error && (
                                    <small className="scan-error">
                                      {job.error}
                                    </small>
                                  )}
                                </div>
                                {["queued", "running", "cancelling"].includes(
                                  job.state,
                                ) ? (
                                  <>
                                    <LoaderCircle size={16} className="spin" />
                                    <button
                                      className="btn secondary small"
                                      disabled={job.state === "cancelling"}
                                      onClick={() =>
                                        void safe(async () => {
                                          await api(
                                            `preview-jobs/${job.id}/cancel`,
                                            "POST",
                                            {},
                                          );
                                          await refresh();
                                        })
                                      }
                                    >
                                      取消扫描
                                    </button>
                                  </>
                                ) : (
                                  <>
                                    <button
                                      className="btn secondary small"
                                      onClick={() => openScan(job)}
                                    >
                                      {job.state === "ready"
                                        ? "查看预览"
                                        : "重新扫描"}
                                    </button>
                                    <button
                                      className="icon-btn"
                                      aria-label={`收起 ${job.planName} 扫描结果`}
                                      onClick={() =>
                                        setDismissedScans((ids) => [
                                          ...ids,
                                          job.id,
                                        ])
                                      }
                                    >
                                      <X size={16} />
                                    </button>
                                  </>
                                )}
                              </div>
                            ))}
                          </section>
                        ) : (
                          <Empty
                            icon={<Activity size={28} />}
                            title="没有待处理的扫描"
                            text="计划预检查和手动扫描的进度、结果会集中显示在这里。"
                          />
                        )}
                      </>
                    )}
                  </section>
                </>
              )}
              {page === "dashboard" && (
                <>
                  {!snapshots.length && (
                    <div className="setup-intro">
                      <ShieldCheck size={25} />
                      <div>
                        <h3>三步，建立第一份恢复点</h3>
                        <p>检查环境 → 选择目录与磁盘 → 预览后备份</p>
                      </div>
                      <button
                        className="btn primary"
                        onClick={() => setSetupOpen(true)}
                      >
                        开始设置
                      </button>
                    </div>
                  )}
                  {showOverview("progress") && active && (
                    <ResourceMonitor
                      runs={runs}
                      compact
                      onOpen={() => {
                        setDiskTab("activity");
                        setPage("storage");
                      }}
                    />
                  )}
                  {showOverview("health") && (
                    <div className="health-banner">
                      <div className="health-icon">
                        <ShieldCheck size={28} />
                      </div>
                      <div>
                        <h3>
                          {active
                            ? active.status === "paused"
                              ? "备份已暂停，随时可以继续"
                              : active.status === "pausing"
                                ? "正在安全暂停备份"
                                : "正在为你的数据多添一份安心"
                            : latestSuccess
                              ? "已有成功备份记录"
                              : plans.length
                                ? "备份计划已就绪"
                                : "给重要的数据，一个安心的家"}
                        </h3>
                        <p>
                          {active
                            ? `${active.planName} · ${active.stage}`
                            : latestSuccess
                              ? `上次成功备份于 ${date(latestSuccess.finishedAt!)} · 请在下方查看各计划的最新运行结果`
                              : plans.length
                                ? "预览变化后即可开始第一次备份，历史版本会被完整保留。"
                                : "从选择第一个目录开始，创建属于你的自动备份计划。"}
                        </p>
                      </div>
                      <span className="health-pill">
                        <span className="dot" />
                        {active
                          ? (labels[active.status] ?? "备份进行中")
                          : latestSuccess
                            ? "有成功记录"
                            : "本地优先"}
                      </span>
                      <div className="banner-orbit orbit-one" />
                      <div className="banner-orbit orbit-two" />
                    </div>
                  )}
                  {showOverview("stats") && (
                    <div className="stats">
                      <Stat
                        icon={<Layers size={19} />}
                        title="备份计划"
                        value={String(plans.length)}
                        suffix="个"
                        foot={`${plans.filter((p) => p.enabled).length} 个已开启自动备份`}
                        color="green"
                      />
                      <Stat
                        icon={<FileCheck2 size={19} />}
                        title="历史文件版本"
                        value={totalFiles.toLocaleString()}
                        suffix="个"
                        foot="完整快照中的文件与符号链接"
                        color="blue"
                      />
                      <Stat
                        icon={<History size={19} />}
                        title="可恢复快照"
                        value={String(snapshots.length)}
                        suffix="份"
                        foot={
                          snapshots.length
                            ? "每一份都是独立恢复点"
                            : "完成首次备份后自动生成"
                        }
                        color="purple"
                      />
                      <Stat
                        icon={<HardDrive size={19} />}
                        title="已连接磁盘"
                        value={String(
                          volumes.filter((v) => v.connected).length,
                        )}
                        suffix="个"
                        foot="按卷身份识别，避免误写"
                        color="orange"
                      />
                    </div>
                  )}
                  {showOverview("plans") && (
                    <>
                      <SectionTitle
                        title="计划摘要"
                        count={plans.length}
                        sub="快速查看最近结果，点击计划进入管理。"
                        action={
                          <button
                            className="text-btn"
                            onClick={() => setPage("plans")}
                          >
                            查看全部 <ArrowRight size={14} />
                          </button>
                        }
                      />
                      {plans.length ? (
                        <div className="overview-plan-list">
                          {plans.slice(0, 4).map((p) => {
                            const latest = runs.find((r) => r.planId === p.id);
                            return (
                              <button
                                className="overview-plan-row"
                                key={p.id}
                                onClick={() => {
                                  setQuery(p.name);
                                  setFilter("all");
                                  setPage("plans");
                                }}
                              >
                                <Folder size={20} />
                                <span>
                                  <b>{p.name}</b>
                                  <small>
                                    {modeLabels[p.mode]} ·{" "}
                                    {p.sources.filter((s) => s.enabled).length}{" "}
                                    个源目录
                                  </small>
                                </span>
                                <span className="overview-plan-result">
                                  {latest
                                    ? (labels[latest.status] ?? latest.status)
                                    : "尚未备份"}
                                  <small>
                                    {latest
                                      ? date(
                                          latest.finishedAt || latest.startedAt,
                                        )
                                      : "等待首次运行"}
                                  </small>
                                </span>
                                <ChevronRight size={16} />
                              </button>
                            );
                          })}
                        </div>
                      ) : (
                        <Empty
                          icon={<FolderOpen size={29} />}
                          title="还没有备份计划"
                          text="选择需要保护的目录与备份磁盘。"
                          action={
                            <button
                              className="btn primary"
                              onClick={() => setEditor(emptyPlan())}
                            >
                              创建备份计划
                            </button>
                          }
                        />
                      )}
                    </>
                  )}
                  <div
                    className={
                      "dashboard-bottom " +
                      (!(showOverview("activity") && showOverview("storage"))
                        ? "single"
                        : "")
                    }
                  >
                    {showOverview("activity") && (
                      <section>
                        <SectionTitle
                          title="最近活动"
                          action={
                            <button
                              className="text-btn"
                              onClick={() => {
                                setHistoryPlan("");
                                setPage("history");
                              }}
                            >
                              全部记录 <ArrowRight size={14} />
                            </button>
                          }
                        />
                        <ActivityList
                          runs={runs.slice(0, 4)}
                          onClick={setRunDetail}
                        />
                      </section>
                    )}
                    {showOverview("storage") && (
                      <section>
                        <SectionTitle
                          title="磁盘容量摘要"
                          action={
                            <button
                              className="icon-btn"
                              aria-label="查看磁盘与资源"
                              onClick={() => {
                                setDiskTab("capacity");
                                setPage("storage");
                              }}
                            >
                              <ArrowUpRight size={17} />
                            </button>
                          }
                        />
                        <div className="storage-mini">
                          {[...volumes]
                            .sort(
                              (a, b) =>
                                Number(!!b.bindings?.length) -
                                  Number(!!a.bindings?.length) ||
                                Number(b.path.startsWith("/Volumes/")) -
                                  Number(a.path.startsWith("/Volumes/")),
                            )
                            .slice(0, 2)
                            .map((v) => (
                              <VolumeCard key={v.id} volume={v} compact />
                            ))}
                          {!volumes.length && (
                            <p className="muted">暂未发现可访问磁盘</p>
                          )}
                          <div className="storage-tip">
                            <ShieldCheck size={15} />{" "}
                            连接外置磁盘，建立更独立的安全副本。
                          </div>
                        </div>
                      </section>
                    )}
                  </div>
                  {!overview.length && (
                    <Empty
                      icon={<LayoutDashboard size={28} />}
                      title="按你的习惯安排概览"
                      text="当前未选择任何概览内容，可随时重新开启。"
                      action={
                        <button
                          className="btn secondary"
                          onClick={() => setCustomizeOverview(true)}
                        >
                          选择显示内容
                        </button>
                      }
                    />
                  )}
                  <div className="page-foot">
                    <LockKeyhole size={13} /> 本地运行 · 离线可用 ·
                    你的文件不会上传至任何服务器
                  </div>
                </>
              )}
              {page === "plans" && (
                <>
                  <div className="toolbar">
                    <div className="segmented">
                      {[
                        ["all", "全部计划"],
                        ["enabled", "自动备份开启"],
                        ["paused", "仅手动备份"],
                      ].map(([v, l]) => (
                        <button
                          key={v}
                          className={filter === v ? "selected" : ""}
                          onClick={() => setFilter(v)}
                        >
                          {l}
                          {v === "all" && <span>{plans.length}</span>}
                        </button>
                      ))}
                    </div>
                    <div className="search">
                      <Search size={16} />
                      <input
                        placeholder="搜索计划或目录…"
                        value={query}
                        onChange={(e) => setQuery(e.target.value)}
                      />
                    </div>
                  </div>
                  <div className="plan-grid">
                    {plans
                      .filter(
                        (p) =>
                          (filter === "all" ||
                            p.enabled === (filter === "enabled")) &&
                          [p.name, ...p.sources.map((s) => s.path)]
                            .join()
                            .toLowerCase()
                            .includes(query.toLowerCase()),
                      )
                      .map((p) => (
                        <PlanCard
                          key={p.id}
                          plan={p}
                          run={
                            runs.find(
                              (r) => r.planId === p.id && !r.finishedAt,
                            ) || runs.find((r) => r.planId === p.id)
                          }
                          snapshots={snapshots.filter((s) => s.planId === p.id)}
                          onRunDetail={(run) => setRunDetail(run)}
                          onControl={controlRun}
                          onCancel={cancelRun}
                          onEdit={() => setEditor(structuredClone(p))}
                          onPreview={() => void showPreview(p)}
                          onSourcePreview={(ids) => void showPreview(p, ids)}
                          onChooseSources={() =>
                            setSourcePicker({
                              plan: p,
                              ids: p.sources
                                .filter((s) => s.enabled)
                                .map((s) => s.id),
                            })
                          }
                          onToggle={() => void toggle(p)}
                          onDelete={() => deletePlan(p)}
                          onHistory={() => {
                            setHistoryPlan(p.id);
                            setPage("history");
                          }}
                        />
                      ))}
                  </div>
                  {!plans.length && (
                    <Empty
                      icon={<Layers size={30} />}
                      title="计划好，就安心了"
                      text="一个计划可包含多个目录，也可以同时备份到多个磁盘。"
                      action={
                        <button
                          className="btn primary"
                          onClick={() => setEditor(emptyPlan())}
                        >
                          新建计划 <Plus size={16} />
                        </button>
                      }
                    />
                  )}
                </>
              )}
              {page === "history" && (
                <>
                  <div className="toolbar">
                    <span className="muted">
                      可恢复快照 ·{" "}
                      {
                        snapshots.filter(
                          (s) => !historyPlan || s.planId === historyPlan,
                        ).length
                      }
                    </span>
                    <Select
                      aria-label="按计划筛选"
                      value={historyPlan}
                      onChange={(e) => setHistoryPlan(e.target.value)}
                    >
                      <option value="">全部备份计划</option>
                      {Array.from(
                        new Map([
                          ...plans.map((p) => [p.id, p.name] as const),
                          ...snapshots.map(
                            (s) => [s.planId, s.planName] as const,
                          ),
                        ]).entries(),
                      ).map(([id, name]) => (
                        <option key={id} value={id}>
                          {name}
                        </option>
                      ))}
                    </Select>
                  </div>
                  <div className="snapshot-list">
                    {snapshots
                      .filter((s) => !historyPlan || s.planId === historyPlan)
                      .map((s) => (
                        <div className="snapshot-row" key={s.id}>
                          <div className="snapshot-icon">
                            <History size={21} />
                          </div>
                          <div className="snapshot-info">
                            <b>
                              {s.planName}
                              {s.pinned && <Pin size={13} />}
                            </b>
                            <span>
                              {date(s.createdAt)} <i>·</i> {s.destinationName}{" "}
                              <i>·</i> {s.files} 个文件
                            </span>
                            {s.updatedSourceIds?.length ? (
                              <small className="scope-history">
                                本次更新：
                                {s.updatedSourceIds
                                  .map(
                                    (id) =>
                                      s.sources?.find((src) => src.id === id)
                                        ?.name || id,
                                  )
                                  .join("、")}{" "}
                                · 其他目录沿用旧版本
                              </small>
                            ) : null}
                          </div>
                          <div className="snapshot-size">
                            {bytes(s.bytes)}
                            <small>逻辑容量</small>
                          </div>
                          <Badge kind="success">已校验</Badge>
                          <button
                            className="icon-btn"
                            aria-label={
                              s.pinned ? "取消永久保留" : "永久保留快照"
                            }
                            title={s.pinned ? "取消永久保留" : "永久保留"}
                            onClick={() =>
                              void safe(async () => {
                                await api(`snapshots/${s.id}`, "PATCH", {
                                  pinned: !s.pinned,
                                });
                                await refresh();
                              })
                            }
                          >
                            <Pin
                              size={16}
                              fill={s.pinned ? "currentColor" : "none"}
                            />
                          </button>
                          <button
                            className="btn secondary small"
                            onClick={() => setFileSnapshot(s)}
                          >
                            <FolderOpen size={15} />
                            浏览与恢复
                          </button>
                        </div>
                      ))}
                  </div>
                  {!snapshots.length && (
                    <Empty
                      icon={<History size={30} />}
                      title="你的时间胶囊，等待第一份备份"
                      text="完成备份后，在这里浏览历史文件，安全恢复到新位置。"
                    />
                  )}
                  <SectionTitle title="运行记录" sub="每一次备份都有迹可循。" />
                  <ActivityList runs={runs} onClick={setRunDetail} />
                </>
              )}
              {page === "storage" && (
                <>
                  <div
                    className="section-tabs"
                    onKeyDown={navigateTabs}
                    role="tablist"
                    aria-label="磁盘与资源分类"
                  >
                    {(
                      [
                        ["capacity", "容量与绑定"],
                        ["activity", "实时读写"],
                        ["retention", "版本清理"],
                      ] as const
                    ).map(([id, label]) => (
                      <button
                        key={id}
                        role="tab"
                        tabIndex={diskTab === id ? 0 : -1}
                        aria-selected={diskTab === id}
                        onClick={() => setDiskTab(id)}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                  {diskTab === "activity" && (
                    <section role="tabpanel" aria-label="实时读写">
                      <ResourceMonitor runs={runs} />
                    </section>
                  )}
                  {diskTab === "capacity" && (
                    <section role="tabpanel" aria-label="容量与绑定">
                      <SectionTitle
                        title="硬盘与计划绑定"
                        sub="选择目标并保存计划后，自动记住卷 UUID。磁盘改名或换接口仍可识别；不同硬盘分别绑定，轮换插入即可备份。"
                      />
                      <div className="volume-grid">
                        {volumes.map((v) => (
                          <VolumeCard
                            key={v.id}
                            volume={v}
                            onPlan={(id) => {
                              const plan = plans.find((p) => p.id === id);
                              if (plan) setEditor(structuredClone(plan));
                            }}
                          />
                        ))}
                      </div>
                    </section>
                  )}
                  {diskTab === "retention" && (
                    <section role="tabpanel" aria-label="版本清理">
                      <div className="notice">
                        <ShieldCheck size={18} />
                        <span>
                          历史快照可能通过硬链接共享内容。逻辑容量不代表实际占用，删除快照释放的空间可能更少。
                        </span>
                      </div>
                      <SectionTitle
                        title="版本保留策略"
                        sub="保留重要的历史，让磁盘空间有序使用。"
                      />
                      <div className="settings-box">
                        {plans.length ? (
                          plans.map((p) => (
                            <div className="setting-row" key={p.id}>
                              <div className="setting-icon">
                                <Layers size={20} />
                              </div>
                              <div>
                                <b>{p.name}</b>
                                <p>
                                  最近 {p.retention.keepLast} 份
                                  {p.retention.keepDays
                                    ? ` / ${p.retention.keepDays} 天内`
                                    : ""}
                                  ；超出任一条件可清理。
                                  {p.retention.keepDaily ||
                                  p.retention.keepWeekly ||
                                  p.retention.keepMonthly
                                    ? ` 另保留每日 ${p.retention.keepDaily || 0}、每周 ${p.retention.keepWeekly || 0}、每月 ${p.retention.keepMonthly || 0} 份代表版本。`
                                    : ""}
                                  最新、当前视图和永久保留版本始终保护。
                                </p>
                              </div>
                              <button
                                className="btn secondary small"
                                onClick={() =>
                                  void safe(async () => {
                                    const list = await api<Snapshot[]>(
                                      "retention/preview",
                                      "POST",
                                      { planId: p.id },
                                    );
                                    if (!list.length) {
                                      notify("暂无符合清理条件的快照");
                                      return;
                                    }
                                    setConfirm({
                                      title: `清理 ${list.length} 份历史快照？`,
                                      text: "这些快照已超出当前保留策略。最新快照、当前视图、永久保留和分层代表版本会保留。此操作无法撤销。",
                                      action: async () => {
                                        await api("retention/apply", "POST", {
                                          planId: p.id,
                                          ids: list.map((s) => s.id),
                                          confirm: true,
                                        });
                                        await refresh();
                                        notify("历史快照已清理");
                                      },
                                    });
                                  })
                                }
                              >
                                预览清理 <ChevronRight size={14} />
                              </button>
                            </div>
                          ))
                        ) : (
                          <p className="muted padded">
                            创建备份计划后，可在这里管理保留策略。
                          </p>
                        )}
                      </div>
                    </section>
                  )}
                </>
              )}
              {page === "settings" && (
                <SettingsPage
                  plans={plans}
                  status={status}
                  notify={notify}
                  refresh={refresh}
                />
              )}
            </>
          )}
        </div>
      </main>
      {setupOpen && (
        <GettingStarted
          plans={plans}
          onClose={() => setSetupOpen(false)}
          onCreate={() => {
            setSetupOpen(false);
            setEditor(emptyPlan());
          }}
          onEdit={(p) => {
            setSetupOpen(false);
            setEditor(structuredClone(p));
          }}
          onPreview={(p) => {
            setSetupOpen(false);
            void showPreview(p);
          }}
          onHistory={(id) => {
            setSetupOpen(false);
            setHistoryPlan(id);
            setPage("history");
          }}
          onSettings={() => {
            setSetupOpen(false);
            setPage("settings");
          }}
        />
      )}
      {customizeOverview && (
        <Modal
          title="自定义概览"
          subtitle="选择概览中显示的内容，设置自动保存在当前浏览器。"
          onClose={() => setCustomizeOverview(false)}
        >
          <div className="overview-options">
            {overviewSections.map((section) => (
              <label key={section.id}>
                <input
                  type="checkbox"
                  checked={overview.includes(section.id)}
                  onChange={(e) =>
                    setOverview((current) =>
                      e.target.checked
                        ? [...current, section.id]
                        : current.filter((id) => id !== section.id),
                    )
                  }
                />
                <span>
                  <b>{section.title}</b>
                  <small>{section.detail}</small>
                </span>
              </label>
            ))}
          </div>
          <div className="modal-footer">
            <button
              className="btn secondary"
              onClick={() => setOverview([...defaultOverview])}
            >
              恢复默认
            </button>
            <button
              className="btn primary"
              onClick={() => setCustomizeOverview(false)}
            >
              完成
            </button>
          </div>
        </Modal>
      )}
      {editor && (
        <PlanEditor
          plan={editor}
          onClose={() => setEditor(null)}
          notify={notify}
          onSave={async (p) => {
            const saved = await api<Plan>(
              p.id ? `plans/${p.id}` : "plans",
              p.id ? "PATCH" : "POST",
              p,
            );
            setEditor(null);
            await refresh();
            notify(
              !p.id && !saved.enabled
                ? "计划已保存，正在后台准备首次备份预览"
                : "备份计划已保存",
            );
            if (!p.id && !saved.enabled)
              void showPreview(saved, undefined, true);
          }}
        />
      )}
      {sourcePicker && (
        <Modal
          title="选择目录备份"
          subtitle={sourcePicker.plan.name}
          onClose={() => setSourcePicker(null)}
        >
          <p className="muted">
            只扫描与更新所选目录；其他目录沿用上次备份。此操作不会修改计划中的启用设置。
          </p>
          <div className="source-scope-list">
            {sourcePicker.plan.sources.map((src) => (
              <label key={src.id} className={!src.enabled ? "disabled" : ""}>
                <input
                  type="checkbox"
                  disabled={!src.enabled}
                  checked={sourcePicker.ids.includes(src.id)}
                  onChange={(e) =>
                    setSourcePicker({
                      ...sourcePicker,
                      ids: e.target.checked
                        ? [...sourcePicker.ids, src.id]
                        : sourcePicker.ids.filter((id) => id !== src.id),
                    })
                  }
                />
                <Folder size={18} />
                <span>
                  <b>{src.name}</b>
                  <small>
                    {src.path}
                    {!src.enabled ? " · 未参与备份" : ""}
                  </small>
                </span>
              </label>
            ))}
          </div>
          <div className="modal-footer">
            <small className="muted">
              已选择 {sourcePicker.ids.length} 个目录
            </small>
            <button
              className="btn secondary"
              onClick={() => setSourcePicker(null)}
            >
              取消
            </button>
            <button
              className="btn primary"
              disabled={!sourcePicker.ids.length}
              onClick={() => {
                const selection = sourcePicker;
                setSourcePicker(null);
                void showPreview(selection.plan, selection.ids);
              }}
            >
              预览所选目录
            </button>
          </div>
        </Modal>
      )}
      {preview && (
        <Modal
          title="备份变更预览"
          subtitle={preview.plan.name}
          onClose={() => setPreview(null)}
          wide
        >
          {preview.loading ? (
            <>
              <div className="loading-state">
                <LoaderCircle className="spin" />
                {scanJobs.find((j) => j.id === preview.jobId)?.stage ||
                  "正在提交后台扫描…"}
              </div>
              <p className="muted">
                可以关闭窗口继续操作，扫描不会中断；结果会显示在「任务中心 →
                扫描与预览」中。不会自动开始备份。
              </p>
              <div className="modal-footer">
                <button
                  className="btn secondary"
                  onClick={() => setPreview(null)}
                >
                  放到后台
                </button>
                {preview.jobId && !preview.jobId.startsWith("request-") && (
                  <button
                    className="btn danger"
                    onClick={() =>
                      void safe(async () => {
                        await api(
                          `preview-jobs/${preview.jobId}/cancel`,
                          "POST",
                          {},
                        );
                        setPreview(null);
                        await refresh();
                      })
                    }
                  >
                    取消扫描
                  </button>
                )}
              </div>
            </>
          ) : (
            preview.value && (
              <>
                {preview.sourceIds?.length ? (
                  <div className="notice">
                    <Folder size={17} />
                    <span>
                      本次仅更新：
                      {preview.plan.sources
                        .filter((s) => preview.sourceIds?.includes(s.id))
                        .map((s) => s.name)
                        .join("、")}
                      。其他目录保留上次版本。
                    </span>
                  </div>
                ) : null}
                <div className="preview-stats">
                  {[
                    ["新增", preview.value.added, "green"],
                    ["修改", preview.value.modified, "blue"],
                    ["移动", preview.value.moved, "purple"],
                    [
                      "移除",
                      preview.plan.mode === "mirror"
                        ? preview.value.deletePaths
                        : preview.value.deleted,
                      "orange",
                    ],
                  ].map(([label, n, color]) => (
                    <div className={String(color)} key={String(label)}>
                      <strong>{n}</strong>
                      <span>{label}</span>
                    </div>
                  ))}
                </div>
                <div className="preview-summary">
                  <span>
                    预计传输 <b>{bytes(preview.value.transferBytes)}</b>
                  </span>
                  <span>
                    已复用 <b>{preview.value.reused} 个</b>
                  </span>
                  <span>
                    逻辑容量 <b>{bytes(preview.value.bytes)}</b>
                  </span>
                  <span>
                    模式 <b>{modeLabels[preview.plan.mode]}</b>
                  </span>
                </div>
                {preview.value.retained > 0 && (
                  <div className="notice compact">
                    <ShieldCheck size={16} />
                    保留 {preview.value.retained} 个旧文件，
                    {bytes(preview.value.retainedBytes)}
                    。它们不会被从同步视图移除。
                  </div>
                )}
                <PreviewOperations
                  key={preview.value.previewId || preview.plan.id}
                  operations={
                    preview.plan.mode === "mirror"
                      ? preview.value.operations.filter(
                          (o) => o.type !== "deleted",
                        )
                      : preview.value.operations
                  }
                  plan={preview.plan}
                />
                {preview.plan.mode === "mirror" &&
                  preview.value.deletePaths > 0 && (
                    <PreviewOperations
                      key={"removals-" + preview.value.previewId}
                      operations={preview.value.removals || []}
                      plan={preview.plan}
                      destructive
                    />
                  )}
                <FingerprintStats value={preview.value.scanStats} />
                {preview.value.warnings.map((w) => (
                  <div key={w} className="notice compact">
                    <AlertCircle size={15} />
                    {w}
                  </div>
                ))}
                {preview.plan.mode === "mirror" &&
                  preview.value.deletePaths > 0 && (
                    <div className="mirror-confirm">
                      <b>
                        同步视图将移除 {preview.value.deletePaths} 项 ·{" "}
                        {bytes(preview.value.deleteBytes)}
                      </b>
                      <p>
                        本次预览有效期至{" "}
                        {preview.value.expiresAt
                          ? date(preview.value.expiresAt)
                          : "重新扫描前"}
                        。目录、规则或目标变化后需要重新预览。
                      </p>
                      <label>
                        <input
                          type="checkbox"
                          checked={!!preview.confirmed}
                          onChange={(e) =>
                            setPreview({
                              ...preview,
                              confirmed: e.target.checked,
                            })
                          }
                        />
                        我已检查移除列表，确认从当前同步视图移除这些路径；历史版本保留。
                      </label>
                    </div>
                  )}
                <div className="modal-footer">
                  <span>
                    <ShieldCheck size={15} />
                    历史版本始终保留
                  </span>
                  <button
                    className="btn secondary"
                    onClick={() => setPreview(null)}
                  >
                    取消
                  </button>
                  <button
                    className="btn primary"
                    disabled={
                      busy ||
                      (preview.plan.mode === "mirror" &&
                        preview.value.deletePaths > 0 &&
                        !preview.confirmed)
                    }
                    onClick={() => void start(preview.plan)}
                  >
                    {busy ? (
                      <LoaderCircle size={16} className="spin" />
                    ) : (
                      <Play size={15} />
                    )}
                    {status?.activeRunId ? "加入备份队列" : "开始备份"}
                  </button>
                </div>
              </>
            )
          )}
        </Modal>
      )}
      {runDetail && (
        <RunModal
          run={runs.find((r) => r.id === runDetail.id) || runDetail}
          onClose={() => setRunDetail(null)}
          onReview={() => {
            const plan = plans.find((p) => p.id === runDetail.planId);
            if (plan) {
              setRunDetail(null);
              void showPreview(plan, runDetail.sourceIds);
            }
          }}
          onControl={controlRun}
          onCancel={cancelRun}
        />
      )}
      {fileSnapshot && (
        <FilesModal
          snapshot={fileSnapshot}
          onClose={() => setFileSnapshot(null)}
          notify={notify}
        />
      )}
      {confirm && (
        <Modal title={confirm.title} onClose={() => setConfirm(null)}>
          <p className="confirm-text">{confirm.text}</p>
          <div className="modal-footer">
            <button className="btn secondary" onClick={() => setConfirm(null)}>
              取消
            </button>
            <button
              className="btn danger"
              disabled={busy}
              onClick={() => {
                setBusy(true);
                void safe(async () => {
                  await confirm.action();
                  setConfirm(null);
                }).finally(() => setBusy(false));
              }}
            >
              {busy ? <LoaderCircle size={15} className="spin" /> : null}
              确认操作
            </button>
          </div>
        </Modal>
      )}
      {toast && (
        <div className={"toast " + (toast.error ? "error" : "")} role="status">
          {toast.error ? <AlertCircle size={18} /> : <CheckCircle2 size={18} />}
          <span>{toast.text}</span>
          <button onClick={() => setToast(null)} aria-label="关闭提示">
            <X size={15} />
          </button>
        </div>
      )}
    </div>
  );
}
function Logo() {
  return (
    <span className="logo">
      <img src="/brand-mark.svg" alt="" aria-hidden="true" />
    </span>
  );
}
function Badge({
  kind,
  children,
}: {
  kind: string;
  children: React.ReactNode;
}) {
  return (
    <span className={"badge " + kind}>
      <span className="dot" />
      {children}
    </span>
  );
}
function Stat({
  icon,
  title,
  value,
  suffix,
  foot,
  color,
}: {
  icon: React.ReactNode;
  title: string;
  value: string;
  suffix: string;
  foot: string;
  color: string;
}) {
  return (
    <div className="stat">
      <div className="stat-top">
        <span>{title}</span>
        <span className={"stat-icon " + color}>{icon}</span>
      </div>
      <div className="stat-value">
        {value}
        <span>{suffix}</span>
      </div>
      <div className="stat-foot">{foot}</div>
    </div>
  );
}
function SectionTitle({
  title,
  count,
  sub,
  action,
}: {
  title: string;
  count?: number;
  sub?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="section-title">
      <div>
        <h2>
          {title}
          {count !== undefined && <span>{count}</span>}
        </h2>
        {sub && <p>{sub}</p>}
      </div>
      {action}
    </div>
  );
}
function Empty({
  icon,
  title,
  text,
  action,
}: {
  icon: React.ReactNode;
  title: string;
  text: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-icon">{icon}</div>
      <h3>{title}</h3>
      <p>{text}</p>
      {action}
    </div>
  );
}
function PlanCard({
  plan: p,
  run,
  snapshots,
  onEdit,
  onPreview,
  onSourcePreview,
  onChooseSources,
  onToggle,
  onDelete,
  onHistory,
  onRunDetail,
  onControl,
  onCancel,
}: {
  plan: Plan;
  run?: Run;
  onRunDetail: (run: Run) => void;
  onControl: (id: string, action: "pause" | "resume") => void;
  onCancel: (id: string) => void;
  snapshots: Snapshot[];
  onEdit: () => void;
  onPreview: () => void;
  onSourcePreview: (ids: string[]) => void;
  onChooseSources: () => void;
  onToggle: () => void;
  onDelete: () => void;
  onHistory: () => void;
}) {
  const [menu, setMenu] = useState(false);
  const current =
    !!run &&
    [
      "running",
      "paused",
      "pausing",
      "queued",
      "waiting_for_volume",
      "waiting_for_source",
    ].includes(run.status);
  const size = snapshots[0]?.bytes || 0;
  return (
    <article className="plan-card">
      <div className="plan-top">
        <div className="plan-symbol">
          <Folder size={23} />
        </div>
        <div>
          <button className="plan-name" onClick={onEdit}>
            {p.name}
          </button>
          <div className="plan-sub">
            {p.sources.filter((s) => s.enabled).length} 个源目录<span>·</span>
            {modeLabels[p.mode] || p.mode}
          </div>
        </div>
        <div className="plan-menu">
          <button
            className="icon-btn"
            aria-label={`${p.name}更多操作`}
            onClick={() => setMenu(!menu)}
          >
            <MoreHorizontal size={20} />
          </button>
          {menu && (
            <>
              <div className="menu-dismiss" onClick={() => setMenu(false)} />
              <div className="dropdown">
                {[
                  { text: "编辑计划", icon: SlidersHorizontal, fn: onEdit },
                  { text: "选择目录备份", icon: Folder, fn: onChooseSources },
                  {
                    text: p.enabled ? "关闭自动备份" : "开启自动备份",
                    icon: p.enabled ? Pause : Play,
                    fn: onToggle,
                  },
                  { text: "查看历史版本", icon: History, fn: onHistory },
                  { text: "删除计划", icon: Trash2, fn: onDelete },
                ].map(({ text, icon: Icon, fn }) => (
                  <button
                    key={text}
                    onClick={() => {
                      setMenu(false);
                      fn();
                    }}
                  >
                    <Icon size={14} />
                    {text}
                  </button>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
      <div className="source-pills">
        {p.sources.slice(0, 3).map((s) => (
          <button
            key={s.id}
            disabled={!s.enabled}
            onClick={() => onSourcePreview([s.id])}
            title={s.enabled ? `预览目录 ${s.name}` : "此目录未参与备份"}
            aria-label={`预览目录 ${s.name}`}
          >
            <Folder size={13} />
            {s.name}
          </button>
        ))}
        {p.sources.length > 3 && <span>+{p.sources.length - 3}</span>}
      </div>
      <div className="destination-line">
        <HardDrive size={15} />
        <span>
          {p.destinations
            .filter((d) => d.enabled)
            .map((d) => d.name)
            .join("、")}
        </span>
        <span className="snapshot-label">{snapshots.length} 个恢复点</span>
      </div>
      <div className="plan-metrics">
        <div>
          <small>备份逻辑容量</small>
          <b>{snapshots.length ? bytes(size) : "等待首次备份"}</b>
        </div>
        <div>
          <small>自动备份</small>
          <b>{scheduleLabel(p)}</b>
        </div>
      </div>
      {current && (
        <div className="progress-track">
          <span style={{ width: run.progress + "%" }} />
        </div>
      )}
      <div className="plan-bottom">
        <span
          className={
            "plan-status " +
            (["failed", "recoverable", "needs_confirmation"].includes(
              run?.status ?? "",
            )
              ? "warn"
              : "")
          }
        >
          {current ? (
            run.status === "paused" ? (
              <Pause size={13} />
            ) : run.status === "queued" ||
              run.status === "waiting_for_volume" ||
              run.status === "waiting_for_source" ? (
              <Clock size={13} />
            ) : (
              <LoaderCircle size={13} className="spin" />
            )
          ) : run?.status === "success" ? (
            <CheckCircle2 size={13} />
          ) : (
            <span className={"dot " + (!p.enabled ? "gray" : "")} />
          )}
          <span>
            {current
              ? (labels[run.status] ?? "备份进行中")
              : run?.status === "failed"
                ? "上次备份失败，可重新预览重试"
                : run?.status === "success"
                  ? `上次备份成功 · ${date(run.finishedAt!)}`
                  : run
                    ? lastRunLabel(run.status)
                    : "尚未执行备份"}
          </span>
        </span>
        <div className="run-card-actions">
          {current && run ? (
            <>
              {run.status === "paused" ? (
                <button
                  className="btn primary small"
                  onClick={() => onControl(run.id, "resume")}
                >
                  <Play size={14} />
                  继续备份
                </button>
              ) : (
                run.status !== "pausing" && (
                  <button
                    className="btn secondary small"
                    onClick={() => onControl(run.id, "pause")}
                  >
                    <Pause size={14} />
                    暂停本次
                  </button>
                )
              )}
              <button
                className="btn danger small"
                onClick={() => onCancel(run.id)}
              >
                取消本次
              </button>
              <button
                className="btn secondary small"
                onClick={() => onRunDetail(run)}
              >
                运行详情
              </button>
            </>
          ) : (
            <>
              {run &&
                [
                  "failed",
                  "recoverable",
                  "cancelled",
                  "needs_confirmation",
                ].includes(run.status) && (
                  <button
                    className="btn secondary small"
                    onClick={() => onRunDetail(run)}
                  >
                    查看详情
                  </button>
                )}
              <button className="btn secondary small" onClick={onPreview}>
                {run?.status === "needs_confirmation"
                  ? "重新预览并确认"
                  : run &&
                      ["failed", "recoverable", "cancelled"].includes(
                        run.status,
                      )
                    ? "重新预览并重试"
                    : "预览备份"}
                <ArrowRight size={14} />
              </button>
            </>
          )}
        </div>
      </div>
    </article>
  );
}
function ActivityList({
  runs,
  onClick,
}: {
  runs: Run[];
  onClick: (r: Run) => void;
}) {
  return (
    <div className="activity-list">
      {runs.length ? (
        runs.map((r) => (
          <button
            key={r.id}
            className="activity-row"
            onClick={() => onClick(r)}
          >
            <span className={"activity-icon " + r.status}>
              {r.status === "success" ? (
                <Check size={17} />
              ) : r.status === "running" || r.status === "pausing" ? (
                <LoaderCircle className="spin" size={17} />
              ) : r.status === "paused" ? (
                <Pause size={17} />
              ) : r.status === "queued" ||
                r.status === "waiting_for_volume" ||
                r.status === "waiting_for_source" ? (
                <Clock size={17} />
              ) : (
                <AlertCircle size={17} />
              )}
            </span>
            <span className="activity-info">
              <b>
                {r.planName}
                <span>{labels[r.status] || r.status}</span>
              </b>
              <small>
                {r.status === "cancelled"
                  ? "本次任务已结束，已有恢复点保留"
                  : r.error || r.stage}
              </small>
            </span>
            <time>{date(r.startedAt)}</time>
            <ChevronRight size={15} />
          </button>
        ))
      ) : (
        <div className="quiet-empty">
          <Clock size={23} />
          <b>新的安心，从这里开始</b>
          <p>备份活动会在这里呈现</p>
        </div>
      )}
    </div>
  );
}
function VolumeCard({
  volume: v,
  compact = false,
  onPlan,
}: {
  onPlan?: (id: string) => void;
  volume: Volume;
  compact?: boolean;
}) {
  const used = v.capacity - v.free,
    ratio = v.capacity ? Math.round((used / v.capacity) * 100) : 0;
  return (
    <div className={"volume-card " + (compact ? "compact" : "")}>
      <div className="volume-top">
        <span className="disk-icon">
          <HardDrive size={compact ? 21 : 25} />
        </span>
        <div>
          <b>{v.name}</b>
          <small>{compact ? "本地磁盘" : v.path}</small>
        </div>
        <span className={"volume-state " + (!v.connected ? "offline" : "")}>
          <span className="dot" />
          {v.connected ? "已连接" : "未连接"}
        </span>
      </div>
      {v.connected && (
        <>
          <div className="disk-bar">
            <span style={{ width: ratio + "%" }} />
          </div>
          <div className="volume-capacity">
            <span>
              {bytes(used)} <small>/ {bytes(v.capacity)}</small>
            </span>
            <span>{bytes(v.free)} 可用</span>
          </div>
        </>
      )}
      {!compact && (
        <div className="volume-id">
          卷标识 <code>{v.id}</code>
        </div>
      )}
      {!compact && v.bindings?.length ? (
        <div className="disk-bindings">
          {v.bindings.map((b) => (
            <button
              className="disk-binding"
              key={b.destinationId + b.planId}
              onClick={() => onPlan?.(b.planId)}
            >
              <span>
                <b>{b.planName}</b>
                <small>卷内目录 · {b.relativePath || "."}</small>
              </span>
              <span className={b.automatic ? "binding-auto" : ""}>
                {b.automatic
                  ? "插入自动备份"
                  : b.enabled
                    ? "自动触发未开启"
                    : "目标已停用"}
              </span>
            </button>
          ))}
        </div>
      ) : (
        !compact && (
          <p className="disk-unbound">
            尚未绑定计划 · 在计划中选择此磁盘上的备份目录
          </p>
        )
      )}
    </div>
  );
}
function Modal({
  title,
  subtitle,
  children,
  onClose,
  wide = false,
}: {
  title: string;
  subtitle?: string;
  children: React.ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  return (
    <div
      className="modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <section
        className={"modal " + (wide ? "wide" : "")}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="modal-header">
          <div>
            <h2>{title}</h2>
            {subtitle && <p>{subtitle}</p>}
          </div>
          <button className="icon-btn" aria-label="关闭弹窗" onClick={onClose}>
            <X size={20} />
          </button>
        </div>
        {children}
      </section>
    </div>
  );
}
function DirectoryPicker({
  initial,
  protectedSources = [],
  onChoose,
  onClose,
}: {
  initial?: string;
  protectedSources?: string[];
  onChoose: (path: string) => void;
  onClose: () => void;
}) {
  const [data, setData] = useState<{
      path: string;
      parent: string;
      identity: Source;
      directories: { name: string; path: string }[];
    } | null>(null),
    [folderName, setFolderName] = useState(""),
    [creating, setCreating] = useState(false),
    [createOpen, setCreateOpen] = useState(false),
    [input, setInput] = useState(initial || ""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false);
  const browse = async (path: string) => {
    setLoading(true);
    setError("");
    try {
      const d = await api<NonNullable<typeof data>>(
        "filesystem/browse?path=" + encodeURIComponent(path),
      );
      setData(d);
      setInput(d.path);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void browse(initial || "");
  }, []);
  return (
    <Modal title="选择本地目录" onClose={onClose}>
      <div className="picker-path">
        <button
          className="icon-btn"
          aria-label="上一级目录"
          disabled={!data || data.path === "/"}
          onClick={() => void browse(data!.parent)}
        >
          <ArrowLeft size={18} />
        </button>
        <input
          aria-label="目录路径"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void browse(input);
            }
          }}
        />
        <button
          className="btn secondary small"
          onClick={() => void browse(input)}
        >
          前往
        </button>
      </div>
      <div className="picker-shortcuts">
        <button onClick={() => void browse("")}>个人目录</button>
        <button onClick={() => void browse("/Volumes")}>外置磁盘</button>
        <button onClick={() => void browse("/")}>Macintosh HD</button>
      </div>
      <div className="picker-new-folder">
        {createOpen ? (
          <>
            <input
              aria-label="新文件夹名称"
              placeholder="新文件夹名称"
              value={folderName}
              onChange={(e) => setFolderName(e.target.value)}
              disabled={creating}
            />
            <button
              className="btn primary small"
              disabled={!folderName.trim() || !data || loading || creating}
              onClick={() =>
                void (async () => {
                  setCreating(true);
                  setError("");
                  try {
                    const created = await api<{ path: string }>(
                      "filesystem/directories",
                      "POST",
                      {
                        parent: data!.identity,
                        name: folderName,
                        protectedSources,
                      },
                    );
                    await browse(created.path);
                    setFolderName("");
                    setCreateOpen(false);
                  } catch (e) {
                    setError((e as Error).message);
                  } finally {
                    setCreating(false);
                  }
                })()
              }
            >
              {creating ? "正在创建…" : "创建并进入"}
            </button>
            <button
              className="btn secondary small"
              disabled={creating}
              onClick={() => setCreateOpen(false)}
            >
              取消新建
            </button>
          </>
        ) : (
          <button
            className="btn secondary small"
            disabled={!data || loading}
            onClick={() => setCreateOpen(true)}
          >
            <Plus size={14} />
            新建文件夹
          </button>
        )}
      </div>
      <div className="picker-list">
        {loading ? (
          <div className="loading-state">
            <LoaderCircle className="spin" />
            读取目录…
          </div>
        ) : data?.directories.length ? (
          data.directories.map((d) => (
            <button key={d.path} onClick={() => void browse(d.path)}>
              <Folder size={18} />
              {d.name}
              <ChevronRight size={15} />
            </button>
          ))
        ) : (
          <p className="muted padded">这个目录中没有子文件夹。</p>
        )}
      </div>
      {error && <div className="notice error">{error}</div>}
      <div className="modal-footer">
        <span className="picker-selected" title={data?.path}>
          {data?.path}
        </span>
        <button className="btn secondary" onClick={onClose}>
          取消
        </button>
        <button
          className="btn primary"
          disabled={!data || loading || !!error}
          onClick={() => onChoose(data!.path)}
        >
          选择此目录 <Check size={15} />
        </button>
      </div>
    </Modal>
  );
}
function PlanEditor({
  plan,
  onClose,
  onSave,
  notify,
}: {
  plan: Plan;
  onClose: () => void;
  onSave: (p: Plan) => Promise<void>;
  notify: (s: string, e?: boolean) => void;
}) {
  const [p, setP] = useState<Plan>(plan),
    [tab, setTab] = useState("sources"),
    [saving, setSaving] = useState(false),
    [advanced, setAdvanced] = useState(false),
    [picker, setPicker] = useState<{
      kind: "source" | "destination";
      index?: number;
      initial?: string;
    } | null>(null),
    [rulesPreview, setRulesPreview] = useState<RulePreview | null>(null),
    [testing, setTesting] = useState(false),
    [error, setError] = useState("");
  const currentPlan = useRef(p);
  currentPlan.current = p;
  const patch = (v: Partial<Plan>) => {
    setRulesPreview(null);
    setP({ ...p, ...v });
  };
  const updateSource = (i: number, v: Partial<Source>) =>
    patch({ sources: p.sources.map((s, j) => (i === j ? { ...s, ...v } : s)) });
  const choose = (path: string) => {
    if (!picker) return;
    const name = path.split("/").pop() || "磁盘";
    if (picker.kind === "source") {
      if (picker.index !== undefined) updateSource(picker.index, { path });
      else
        patch({
          sources: [
            ...p.sources,
            {
              id: "",
              name,
              path,
              targetPath: name,
              enabled: true,
              excludeHidden: false,
              rules: [],
            },
          ],
        });
    } else {
      if (picker.index !== undefined)
        patch({
          destinations: p.destinations.map((d, j) =>
            j === picker.index ? { ...d, path, volumeId: "" } : d,
          ),
        });
      else
        patch({
          destinations: [
            ...p.destinations,
            { id: "", name, path, volumeId: "", enabled: true },
          ],
        });
    }
    setPicker(null);
  };
  return (
    <div
      className="drawer-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <section
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label="备份计划编辑"
      >
        <div className="drawer-header">
          <div className="eyebrow">BACKUP PLAN</div>
          <div>
            <h2>{p.id ? "编辑备份计划" : "创建一个安心的计划"}</h2>
            <button
              className="icon-btn"
              aria-label="关闭计划编辑"
              onClick={onClose}
            >
              <X size={21} />
            </button>
          </div>
          <p>选择重要的目录，为它们安排一个安全的去处。</p>
        </div>
        <div className="drawer-tabs">
          {[
            ["sources", "目录与目标"],
            ["rules", "筛选规则"],
            ["schedule", "调度与保留"],
          ].map(([id, label], i) => (
            <button
              key={id}
              className={tab === id ? "active" : ""}
              onClick={() => setTab(id)}
            >
              <span>{i + 1}</span>
              {label}
            </button>
          ))}
        </div>
        <div className="drawer-body">
          {tab === "sources" && (
            <>
              {!p.id && (
                <div className="notice">
                  <ShieldCheck size={17} />
                  <span>
                    {p.enabled
                      ? "已启用自动触发，保存后会按调度规则运行。"
                      : "新计划默认关闭自动备份。保存后在后台准备预览，再决定开始备份。"}
                  </span>
                </div>
              )}
              <label className="field">
                计划名称
                <input
                  autoFocus
                  placeholder="例如：Mac 日常资料"
                  value={p.name}
                  onChange={(e) => patch({ name: e.target.value })}
                />
              </label>
              <label className="field">
                备注 <small>可选</small>
                <input
                  placeholder="记录这份备份的用途"
                  value={p.description}
                  onChange={(e) => patch({ description: e.target.value })}
                />
              </label>
              <label className="field">
                备份模式
                <Select
                  aria-label="备份模式"
                  value={p.mode}
                  onChange={(e) => patch({ mode: e.target.value })}
                >
                  <option value="snapshot">快照备份 · 推荐</option>
                  <option value="incremental">
                    增量同步 · 保留消失的旧路径
                  </option>
                  <option value="mirror">镜像同步 · 移除需确认</option>
                </Select>
                <small>
                  {p.mode === "snapshot"
                    ? "每次创建当前选中文件的完整恢复点。"
                    : p.mode === "incremental"
                      ? "同步视图保留已删除或被排除的旧路径；新文件优先，类型冲突的旧内容保存在历史中。"
                      : "只镜像已启用源和规则范围；排除项保留。每次移除旧路径前必须预览并确认，自动任务遇到移除会等待你确认。"}
                </small>
              </label>
              <div className="editor-section-title">
                <h3>
                  源目录 <span>{p.sources.length}</span>
                </h3>
                {p.sources.length > 0 && (
                  <button
                    className="text-btn"
                    onClick={() => setPicker({ kind: "source" })}
                  >
                    <Plus size={15} />
                    添加目录
                  </button>
                )}
              </div>
              {!p.sources.length && (
                <button
                  className="add-directory"
                  onClick={() => setPicker({ kind: "source" })}
                >
                  <FolderOpen size={25} />
                  <b>添加需要保护的目录</b>
                  <span>文档、项目、照片，或者任何重要的文件夹</span>
                </button>
              )}
              {p.sources.map((s, i) => (
                <div className="source-edit" key={i}>
                  <div className="source-edit-top">
                    <Folder size={19} />
                    <input
                      aria-label="目录显示名称"
                      value={s.name}
                      onChange={(e) =>
                        updateSource(i, { name: e.target.value })
                      }
                    />
                    <Toggle
                      label={`参与备份：${s.name}`}
                      value={s.enabled}
                      onChange={(v) => updateSource(i, { enabled: v })}
                    />
                    <button
                      className="icon-btn"
                      aria-label="移除源目录"
                      onClick={() =>
                        patch({ sources: p.sources.filter((_, j) => i !== j) })
                      }
                    >
                      <X size={16} />
                    </button>
                  </div>
                  <button
                    className="path-button"
                    onClick={() =>
                      setPicker({ kind: "source", index: i, initial: s.path })
                    }
                  >
                    {s.path}
                    <FolderOpen size={14} />
                  </button>
                  <SourceBinding source={s} />
                  <div className="source-mapping">
                    <span>显示映射</span>
                    <input
                      aria-label="目标相对路径"
                      value={s.targetPath}
                      onChange={(e) =>
                        updateSource(i, { targetPath: e.target.value })
                      }
                    />
                    <span>存储 ID 自动管理</span>
                  </div>
                </div>
              ))}
              <div className="editor-section-title">
                <h3>
                  备份目标 <span>{p.destinations.length}</span>
                </h3>
                {p.destinations.length > 0 && (
                  <button
                    className="text-btn"
                    onClick={() => setPicker({ kind: "destination" })}
                  >
                    <Plus size={15} />
                    添加目标
                  </button>
                )}
              </div>
              {p.destinations.map((d, i) => (
                <div className="destination-edit" key={i}>
                  <div className="source-edit-top">
                    <HardDrive size={20} />
                    <input
                      aria-label="备份目标名称"
                      value={d.name}
                      onChange={(e) =>
                        patch({
                          destinations: p.destinations.map((v, j) =>
                            i === j ? { ...v, name: e.target.value } : v,
                          ),
                        })
                      }
                    />
                    <Toggle
                      label={`使用目标：${d.name}`}
                      value={d.enabled}
                      onChange={(v) =>
                        patch({
                          destinations: p.destinations.map((d, j) =>
                            i === j ? { ...d, enabled: v } : d,
                          ),
                        })
                      }
                    />
                    <button
                      className="icon-btn"
                      aria-label="移除目标"
                      onClick={() =>
                        patch({
                          destinations: p.destinations.filter(
                            (_, j) => i !== j,
                          ),
                        })
                      }
                    >
                      <X size={16} />
                    </button>
                  </div>
                  <button
                    className="path-button"
                    onClick={() =>
                      setPicker({
                        kind: "destination",
                        index: i,
                        initial: d.path,
                      })
                    }
                  >
                    {d.path}
                    <FolderOpen size={14} />
                  </button>
                  <div className="destination-binding">
                    {d.volumeId ? (
                      <>
                        <ShieldCheck size={14} />
                        <span>
                          已绑定卷 UUID <code>{d.volumeId}</code>
                          <small>
                            卷内目录：
                            {(d.volumeRelativePath?.split("/").includes("..")
                              ? d.path
                              : d.volumeRelativePath) || "."}{" "}
                            · 自动识别重新挂载的位置
                          </small>
                        </span>
                      </>
                    ) : (
                      <>
                        <HardDrive size={14} />
                        <span>保存后自动记录当前磁盘的卷 UUID</span>
                      </>
                    )}
                  </div>
                </div>
              ))}
              {!p.destinations.length && (
                <button
                  className="add-directory target"
                  onClick={() => setPicker({ kind: "destination" })}
                >
                  <HardDrive size={24} />
                  <b>选择备份磁盘或目录</b>
                  <span>建议使用独立的外置磁盘</span>
                </button>
              )}
              <div className="notice compact">
                <ShieldCheck size={17} />
                每个源目录独立存储，同名文件不会相互覆盖。已完成的历史版本保持不可变。
              </div>
            </>
          )}
          {tab === "rules" && (
            <>
              <h3>让备份更轻盈</h3>
              <p className="muted">
                源目录规则优先于计划规则。第一条匹配生效；排除目录将跳过整个子树。
              </p>
              <div className="preset-buttons">
                {[
                  ["通用资料", [".DS_Store", "**/*.tmp", "**/.Trash/**"]],
                  [
                    "代码项目",
                    [
                      "**/node_modules/**",
                      "**/.venv/**",
                      "**/dist/**",
                      ".DS_Store",
                    ],
                  ],
                  ["全部照片", [".DS_Store"]],
                ].map(([name, rules]) => (
                  <button
                    key={String(name)}
                    className="btn secondary small"
                    onClick={() =>
                      patch({
                        fileFilters: {
                          ...p.fileFilters,
                          types: name === "全部照片" ? ["image"] : [],
                        },
                        rules: (rules as string[]).map((pattern) => ({
                          action: "exclude",
                          pattern,
                        })),
                      })
                    }
                  >
                    <SlidersHorizontal size={14} />
                    {name}
                  </button>
                ))}
              </div>
              <RulesEditor
                rules={p.rules}
                onChange={(rules) => patch({ rules })}
              />
              <div className="file-policy">
                <h4>计划文件条件</h4>
                <FileFiltersEditor
                  value={p.fileFilters}
                  onChange={(fileFilters) => patch({ fileFilters })}
                />
              </div>
              {p.sources.map((s, i) => (
                <div key={i} className="source-rules">
                  <h4>
                    <Folder size={16} />
                    {s.name}
                  </h4>
                  <div className="setting-inline">
                    <span>忽略隐藏文件</span>
                    <Toggle
                      label={`排除隐藏文件：${s.name}`}
                      value={s.excludeHidden}
                      onChange={(v) => updateSource(i, { excludeHidden: v })}
                    />
                  </div>
                  <RulesEditor
                    rules={s.rules}
                    onChange={(rules) => updateSource(i, { rules })}
                  />
                  <div className="setting-inline">
                    <div>
                      <b>独立文件条件</b>
                      <p className="muted filter-help">
                        关闭时继承计划文件条件；开启后使用此源的完整条件和例外。
                      </p>
                    </div>
                    <Toggle
                      label={`独立文件条件：${s.name}`}
                      value={s.fileFilters != null}
                      onChange={(enabled) =>
                        updateSource(i, {
                          fileFilters: enabled
                            ? structuredClone(p.fileFilters ?? {})
                            : undefined,
                        })
                      }
                    />
                  </div>
                  {s.fileFilters != null && (
                    <FileFiltersEditor
                      value={s.fileFilters}
                      onChange={(fileFilters) =>
                        updateSource(i, { fileFilters })
                      }
                    />
                  )}
                </div>
              ))}
              <button
                className="btn secondary"
                disabled={
                  testing || !p.sources.length || !p.destinations.length
                }
                onClick={() => {
                  setTesting(true);
                  void api<NonNullable<typeof rulesPreview>>(
                    `plans/${p.id || "draft"}/rules/preview`,
                    "POST",
                    p,
                  )
                    .then((result) => {
                      if (currentPlan.current === p) setRulesPreview(result);
                    })
                    .catch((e) => notify(e.message, true))
                    .finally(() => setTesting(false));
                }}
              >
                {testing ? (
                  <LoaderCircle size={15} className="spin" />
                ) : (
                  <Search size={15} />
                )}
                测试规则
              </button>
              {rulesPreview && <RuleResults value={rulesPreview} />}
            </>
          )}
          {tab === "schedule" && (
            <>
              <h3>备份，按你的节奏发生</h3>
              <div className="settings-box">
                <div className="setting-row">
                  <div>
                    <b>自动备份</b>
                    <p>启用后，根据下方规则自动运行</p>
                  </div>
                  <Toggle
                    label="自动备份"
                    value={p.enabled}
                    onChange={(enabled) => patch({ enabled })}
                  />
                </div>
                <div className="setting-row">
                  <div>
                    <b>磁盘连接时备份</b>
                    <p>
                      按卷 UUID
                      识别硬盘；仅备份到本次连接的目标，其他轮换盘无需同时连接。服务需保持运行。
                    </p>
                  </div>
                  <Toggle
                    label="磁盘连接时备份"
                    value={p.schedule.onMount}
                    onChange={(v) =>
                      patch({ schedule: { ...p.schedule, onMount: v } })
                    }
                  />
                </div>
                <div className="setting-row">
                  <div>
                    <b>文件变化后备份</b>
                    <p>通过 macOS 原生事件检测，等待修改稳定后再运行</p>
                  </div>
                  <Toggle
                    label="文件变化后备份"
                    value={p.schedule.onChange}
                    onChange={(v) =>
                      patch({ schedule: { ...p.schedule, onChange: v } })
                    }
                  />
                </div>
              </div>
              <label className="field">
                备份频率
                <Select
                  aria-label="备份频率"
                  value={p.schedule.intervalMinutes}
                  onChange={(e) =>
                    patch({
                      schedule: {
                        ...p.schedule,
                        intervalMinutes: Number(e.target.value),
                      },
                    })
                  }
                >
                  <option value={0}>仅使用其他触发条件 / 手动</option>
                  <option value={60}>每小时</option>
                  <option value={360}>每 6 小时</option>
                  <option value={1440}>每天</option>
                  <option value={10080}>每周</option>
                </Select>
              </label>
              <label className="field">
                变更静默等待（秒）
                <input
                  type="number"
                  min={10}
                  max={86400}
                  value={p.schedule.debounceSeconds}
                  onChange={(e) =>
                    patch({
                      schedule: {
                        ...p.schedule,
                        debounceSeconds: Number(e.target.value),
                      },
                    })
                  }
                />
              </label>
              <div className="editor-section-title">
                <h3>智能变化触发</h3>
              </div>
              <p className="muted schedule-help">
                文件变化稳定后，同时达到启用的数量与大小阈值才触发。0
                表示关闭对应阈值；最长检查会兜底尚未达标的变化。
              </p>
              <div className="two-fields">
                <label className="field">
                  最少变更文件路径（个）
                  <input
                    aria-label="最少变更文件路径"
                    type="number"
                    min={0}
                    max={1000000}
                    step={1}
                    value={p.schedule.minChangedFiles ?? 0}
                    onChange={(e) =>
                      patch({
                        schedule: {
                          ...p.schedule,
                          minChangedFiles: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  最小变化大小（MiB）
                  <input
                    aria-label="最小变化大小 MiB"
                    type="number"
                    min={0}
                    step={0.01}
                    value={(p.schedule.minChangedBytes || 0) / 1048576}
                    onChange={(e) =>
                      patch({
                        schedule: {
                          ...p.schedule,
                          minChangedBytes: Math.round(
                            Number(e.target.value) * 1048576,
                          ),
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  变化触发最短间隔（分钟）
                  <input
                    aria-label="变化触发最短间隔"
                    type="number"
                    min={0}
                    max={525600}
                    value={p.schedule.minIntervalMinutes ?? 1}
                    onChange={(e) =>
                      patch({
                        schedule: {
                          ...p.schedule,
                          minIntervalMinutes: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  最长检查间隔（分钟）
                  <input
                    aria-label="最长检查间隔"
                    type="number"
                    min={0}
                    max={525600}
                    value={p.schedule.maxIntervalMinutes ?? 0}
                    onChange={(e) =>
                      patch({
                        schedule: {
                          ...p.schedule,
                          maxIntervalMinutes: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
              </div>
              <p className="muted schedule-help">
                变化大小按变化路径的新旧逻辑大小较大值计算，并非实际传输量。最短间隔只约束变化触发；手动、磁盘连接和固定频率独立。最长间隔设为
                0 关闭，否则届时完整检查，有变化就备份，无变化不生成恢复点。
              </p>
              <label className="field">
                自动任务最多尝试次数
                <input
                  aria-label="自动任务最多尝试次数"
                  type="number"
                  min={1}
                  max={10}
                  value={p.schedule.maxAttempts ?? 3}
                  onChange={(e) =>
                    patch({
                      schedule: {
                        ...p.schedule,
                        maxAttempts: Number(e.target.value),
                      },
                    })
                  }
                />
                <small>
                  包含首次执行。失败后按 1、2、4…分钟退避，最长 60
                  分钟；等待磁盘或源目录不消耗尝试次数，需要确认的任务等待人工处理；取消只结束本次任务，关闭自动备份可停止后续触发。
                </small>
              </label>
              <ScheduleStatus planId={p.id} />
              <div className="editor-section-title">
                <h3>历史版本保留</h3>
                <History size={18} />
              </div>
              <div className="two-fields">
                <label className="field">
                  最近快照份数
                  <input
                    type="number"
                    min={1}
                    value={p.retention.keepLast}
                    onChange={(e) =>
                      patch({
                        retention: {
                          ...p.retention,
                          keepLast: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  快照保留天数
                  <input
                    type="number"
                    min={0}
                    value={p.retention.keepDays}
                    onChange={(e) =>
                      patch({
                        retention: {
                          ...p.retention,
                          keepDays: Number(e.target.value),
                        },
                      })
                    }
                  />
                  <small>0 表示不按天数限制</small>
                </label>
              </div>
              <details className="retention-tiers">
                <summary>分层保留（可选）</summary>
                <p className="muted">
                  额外保留最近有备份的 N 个日、周、月各自最新的一份。按 UTC
                  日期分组，周一开始；空缺周期不计数，0
                  表示关闭。三个层级取并集，保护不受上述份数和天数限制，每个目标独立计算。
                </p>
                <div className="two-fields">
                  {(
                    [
                      { key: "keepDaily", label: "每日代表版本" },
                      { key: "keepWeekly", label: "每周代表版本" },
                      { key: "keepMonthly", label: "每月代表版本" },
                    ] as const
                  ).map(({ key, label }) => (
                    <label className="field" key={key}>
                      {label}
                      <input
                        type="number"
                        min={0}
                        max={1000}
                        value={p.retention[key] ?? 0}
                        onChange={(e) =>
                          patch({
                            retention: {
                              ...p.retention,
                              [key]: Number(e.target.value),
                            },
                          })
                        }
                      />
                    </label>
                  ))}
                </div>
              </details>
              <div className="notice compact">
                <ShieldCheck size={17} />
                每个目标的最后一份快照、当前同步视图和永久保留版本不会被清理。到「磁盘与资源
                → 版本清理」预览并确认后才会删除。
              </div>
              <button
                className="advanced-button"
                onClick={() => setAdvanced(!advanced)}
              >
                <ChevronDown size={15} />
                高级选项
              </button>
              {advanced && (
                <div className="advanced-info">
                  <b>内容校验：SHA-256</b>
                  <p>
                    备份前与发布前校验内容；纯移动文件验证一致后复用。默认不跟随符号链接。
                  </p>
                  <b>备份模式：{modeLabels[p.mode]}</b>
                  <p>
                    三种模式都保留完整历史。镜像移除需要有效预览和逐次确认；已提交的历史版本保持不可变。
                  </p>
                  <b>macOS 权限</b>
                  <p>
                    若目录访问失败，请在系统设置中授予运行服务的终端或应用文件访问权限。
                  </p>
                </div>
              )}
            </>
          )}
          {error && (
            <div className="notice error">
              <AlertCircle size={16} />
              {error}
            </div>
          )}
        </div>
        <div className="drawer-footer">
          <span>
            <ShieldCheck size={15} />
            文件仅保存在本地
          </span>
          <button className="btn secondary" onClick={onClose}>
            取消
          </button>
          <button
            className="btn primary"
            disabled={saving}
            onClick={() => {
              setError("");
              if (
                !p.name.trim() ||
                !p.sources.length ||
                !p.destinations.length
              ) {
                setError("请输入计划名称，并至少添加一个源目录和目标");
                setTab("sources");
                return;
              }
              setSaving(true);
              void onSave(p)
                .catch((e) => setError(e.message))
                .finally(() => setSaving(false));
            }}
          >
            {saving ? (
              <LoaderCircle className="spin" size={16} />
            ) : (
              <Check size={16} />
            )}
            保存计划
          </button>
        </div>
      </section>
      {picker && (
        <DirectoryPicker
          initial={picker.initial}
          protectedSources={
            picker.kind === "destination"
              ? p.sources.map((source) => source.path)
              : []
          }
          onChoose={choose}
          onClose={() => setPicker(null)}
        />
      )}
    </div>
  );
}
function RulesEditor({
  rules,
  onChange,
}: {
  rules: Plan["rules"];
  onChange: (rules: Plan["rules"]) => void;
}) {
  return (
    <div className="rules-editor">
      {rules.map((r, i) => (
        <div className="rule-edit" key={i}>
          <Select
            aria-label="规则类型"
            value={r.action}
            onChange={(e) =>
              onChange(
                rules.map((r, j) =>
                  i === j
                    ? { ...r, action: e.target.value as "exclude" | "include" }
                    : r,
                ),
              )
            }
          >
            <option value="exclude">排除</option>
            <option value="include">包含</option>
          </Select>
          <input
            aria-label="匹配规则"
            placeholder="例如 **/node_modules/**"
            value={r.pattern}
            onChange={(e) =>
              onChange(
                rules.map((r, j) =>
                  i === j ? { ...r, pattern: e.target.value } : r,
                ),
              )
            }
          />
          <button
            className="icon-btn"
            aria-label="删除规则"
            onClick={() => onChange(rules.filter((_, j) => j !== i))}
          >
            <X size={15} />
          </button>
        </div>
      ))}
      <button
        className="text-btn"
        onClick={() => onChange([...rules, { action: "exclude", pattern: "" }])}
      >
        <Plus size={14} />
        添加规则
      </button>
    </div>
  );
}
function Toggle({
  label,
  value,
  onChange,
}: {
  label: string;
  value: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <button
      type="button"
      className={"toggle " + (value ? "on" : "")}
      role="switch"
      aria-checked={value}
      aria-label={label}
      onClick={() => onChange(!value)}
    >
      <span />
    </button>
  );
}
function RunModal({
  run: r,
  onClose,
  onCancel,
  onControl,
  onReview,
}: {
  run: Run;
  onReview: () => void;
  onClose: () => void;
  onCancel: (id: string) => void;
  onControl: (id: string, action: "pause" | "resume") => void;
}) {
  return (
    <Modal
      title={r.planName}
      subtitle={`运行详情 · ${date(r.startedAt)}`}
      onClose={onClose}
      wide
    >
      {r.automaticAttempt ? (
        <p className="muted">
          自动任务第 {r.automaticAttempt} 次尝试
          {r.retryOf ? " · 失败后重试" : ""}
        </p>
      ) : null}
      <div className="run-progress">
        <div>
          <Badge kind={r.status}>{labels[r.status]}</Badge>
          <b>任务流程 {r.progress}%</b>
        </div>
        <div className="progress-track">
          <span style={{ width: r.progress + "%" }} />
        </div>
        <p>{r.stage}</p>
      </div>
      {["running", "paused", "pausing"].includes(r.status) && (
        <ResourceMonitor runs={[r]} compact />
      )}
      {r.sourceIds?.length ? (
        <div className="notice">
          <Folder size={17} />
          <span>
            本次仅更新：{(r.sourceNames || r.sourceIds).join("、")}
            。其他目录保留上次版本。
          </span>
        </div>
      ) : null}
      {r.destinationIds?.length ? (
        <div className="notice">
          <HardDrive size={17} />
          <span>
            磁盘连接触发 · 本次执行 {r.destinationIds.length}{" "}
            个绑定目标，其他目标保持等待连接。
          </span>
        </div>
      ) : null}
      {r.error && r.status !== "cancelled" && (
        <div className="notice error">
          <AlertCircle size={17} />
          {r.error}
        </div>
      )}
      {r.preview && (
        <div className="preview-summary">
          <span>
            新增 <b>{r.preview.added}</b>
          </span>
          <span>
            修改 <b>{r.preview.modified}</b>
          </span>
          <span>
            移动复用 <b>{r.preview.moved}</b>
          </span>
          {r.preview.deletePaths > 0 && (
            <span>
              视图移除{" "}
              <b>
                {r.preview.deletePaths} 项 · {bytes(r.preview.deleteBytes)}
              </b>
            </span>
          )}
          {r.preview.retained > 0 && (
            <span>
              保留旧文件 <b>{r.preview.retained}</b>
            </span>
          )}
          <span>
            预计传输 <b>{bytes(r.preview.transferBytes)}</b>
          </span>
        </div>
      )}
      <div className="log-box">
        <div>
          <Terminal size={15} />
          执行日志
        </div>
        <pre>
          {r.logs.length
            ? r.logs.join("\n")
            : r.status === "running"
              ? "正在预检查与扫描文件内容…"
              : "本次运行暂无执行日志"}
        </pre>
      </div>
      <div className="modal-footer">
        <small className="muted">运行 ID：{r.id}</small>
        {["needs_confirmation", "failed", "recoverable", "cancelled"].includes(
          r.status,
        ) && (
          <button className="btn primary" onClick={onReview}>
            {r.status === "needs_confirmation"
              ? "重新预览并确认"
              : "重新预览并重试"}
          </button>
        )}
        {[
          "running",
          "queued",
          "waiting_for_volume",
          "waiting_for_source",
        ].includes(r.status) && (
          <button
            className="btn secondary"
            onClick={() => onControl(r.id, "pause")}
          >
            <Pause size={15} />
            暂停
          </button>
        )}
        {r.status === "paused" && (
          <button
            className="btn primary"
            onClick={() => onControl(r.id, "resume")}
          >
            <Play size={15} />
            继续
          </button>
        )}
        {[
          "running",
          "paused",
          "pausing",
          "queued",
          "waiting_for_volume",
          "waiting_for_source",
        ].includes(r.status) && (
          <button className="btn danger" onClick={() => onCancel(r.id)}>
            取消备份
          </button>
        )}
        <button className="btn secondary" onClick={onClose}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
function FilesModal({
  snapshot: s,
  onClose,
  notify,
}: {
  snapshot: Snapshot;
  onClose: () => void;
  notify: (s: string, error?: boolean) => void;
}) {
  const [entries, setEntries] = useState<Entry[]>([]),
    [total, setTotal] = useState(0),
    [source, setSource] = useState(s.sources[0]?.id || ""),
    [search, setSearch] = useState(""),
    [loading, setLoading] = useState(true),
    [picker, setPicker] = useState(false),
    [selected, setSelected] = useState<Entry | null>(null),
    [restoring, setRestoring] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    setLoading(true);
    setError("");
    let stale = false;
    const t = setTimeout(() => {
      void api<{ entries: Entry[]; total: number }>(
        `snapshots/${s.id}/files?sourceId=${source}&search=${encodeURIComponent(search)}`,
      )
        .then((v) => {
          if (!stale) {
            setEntries(v.entries);
            setTotal(v.total);
          }
        })
        .catch((e) => {
          if (!stale) setError(e.message);
        })
        .finally(() => {
          if (!stale) setLoading(false);
        });
    }, 150);
    return () => {
      stale = true;
      clearTimeout(t);
    };
  }, [source, search, s.id]);
  const restore = async (path: string) => {
    setPicker(false);
    setRestoring(true);
    try {
      const v = await api<{ path: string }>("restores", "POST", {
        snapshotId: s.id,
        sourceId: source,
        path: selected?.path || "",
        destination: path,
      });
      notify("恢复成功：" + v.path);
    } catch (e) {
      notify((e as Error).message, true);
    } finally {
      setRestoring(false);
    }
  };
  return (
    <Modal
      title="找回需要的那一份"
      subtitle={`${s.planName} · ${date(s.createdAt)} · ${s.destinationName}`}
      onClose={onClose}
      wide
    >
      <div className="file-toolbar">
        <Select
          aria-label="选择源目录"
          value={source}
          onChange={(e) => {
            setSource(e.target.value);
            setSelected(null);
          }}
        >
          {s.sources.map((v) => (
            <option key={v.id} value={v.id}>
              {v.name}
            </option>
          ))}
        </Select>
        <div className="search">
          <Search size={15} />
          <input
            placeholder="搜索快照中的文件…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>
      {error ? (
        <div className="notice error">{error}</div>
      ) : (
        <div className="file-list">
          {loading ? (
            <div className="loading-state">
              <LoaderCircle className="spin" />
              正在读取快照…
            </div>
          ) : entries.length ? (
            entries.map((e, i) => (
              <button
                key={i}
                className={selected?.path === e.path ? "selected" : ""}
                onClick={() => setSelected(e)}
              >
                {e.kind === "directory" ? (
                  <Folder size={17} />
                ) : (
                  <File size={17} />
                )}
                <span title={e.path}>
                  {e.path}
                  {e.link && <small> → {e.link}</small>}
                </span>
                <small>
                  {e.kind === "directory"
                    ? "文件夹"
                    : e.kind === "symlink"
                      ? "符号链接"
                      : bytes(e.size)}
                </small>
              </button>
            ))
          ) : (
            <div className="quiet-empty">没有匹配的文件</div>
          )}
        </div>
      )}
      <div className="notice compact">
        <ShieldCheck size={16} />
        文件将恢复到新建目录，原文件不会被覆盖。
        {total > 2000
          ? "当前展示前 2000 项，请搜索缩小范围。"
          : `共 ${total} 项。`}
      </div>
      <div className="modal-footer">
        <span className="muted">
          {selected ? selected.path : "未选择文件，将恢复整个源目录"}
        </span>
        {selected && (
          <button className="text-btn" onClick={() => setSelected(null)}>
            取消选择
          </button>
        )}
        <button
          className="btn primary"
          disabled={restoring || loading || !!error}
          onClick={() => setPicker(true)}
        >
          {restoring ? (
            <LoaderCircle className="spin" size={16} />
          ) : (
            <ArrowDownToLine size={16} />
          )}
          恢复{selected ? "所选项目" : "整个目录"}
        </button>
      </div>
      {picker && (
        <DirectoryPicker
          onClose={() => setPicker(false)}
          onChoose={(path) => void restore(path)}
        />
      )}
    </Modal>
  );
}
function recoveryFilterText(f?: FileFilters) {
  if (!f) return "无附加条件";
  const types: Record<string, string> = {
    image: "图片",
    video: "视频",
    audio: "音频",
    document: "文档",
    archive: "压缩包",
    code: "代码",
  };
  return (
    [
      f.minBytes != null ? `至少 ${bytes(f.minBytes)}` : "",
      f.maxBytes != null ? `至多 ${bytes(f.maxBytes)}` : "",
      f.modifiedWithinDays != null ? `最近 ${f.modifiedWithinDays} 天修改` : "",
      f.types?.length
        ? `类型：${f.types.map((t) => types[t] ?? t).join("、")}`
        : "",
      ...(f.exceptions ?? []).map(
        (e) => `${e.action === "include" ? "明确包含" : "明确排除"} ${e.path}`,
      ),
    ]
      .filter(Boolean)
      .join("；") || "无附加条件"
  );
}
function SettingsPage({
  status,
  plans,
  notify,
  refresh,
}: {
  status: Status | null;
  plans: Plan[];
  notify: (s: string, error?: boolean) => void;
  refresh: () => Promise<void>;
}) {
  const [cfg, setCfg] = useState({ rsyncPath: "", pollSeconds: 30 }),
    [busy, setBusy] = useState(false),
    [recoveryPicker, setRecoveryPicker] = useState(false),
    [recovery, setRecovery] = useState<{
      previewId: string;
      plans: Plan[];
      snapshots: number;
      pinned: number;
      warnings: string[];
    } | null>(null),
    [restorePlans, setRestorePlans] = useState(true);
  useEffect(() => {
    void api<typeof cfg>("settings")
      .then(setCfg)
      .catch((e) => notify(e.message, true));
  }, []);
  return (
    <>
      <div className="settings-box">
        <div className="settings-title">
          <span className="setting-icon">
            <Terminal size={19} />
          </span>
          <div>
            <h3>备份引擎</h3>
            <p>由成熟的 rsync 驱动，完整校验每一份备份。</p>
          </div>
          <Badge kind={status?.rsync.available ? "success" : "failed"}>
            {status?.rsync.available ? "可用" : "未找到"}
          </Badge>
        </div>
        <div className="settings-fields">
          <label className="field">
            rsync 可执行文件路径
            <input
              placeholder={status?.rsync.path || "/opt/homebrew/bin/rsync"}
              value={cfg.rsyncPath}
              onChange={(e) => setCfg({ ...cfg, rsyncPath: e.target.value })}
            />
            <small>留空自动检测 Homebrew rsync 和系统 openrsync</small>
          </label>
          <div className="engine-version">
            <code>{status?.rsync.version}</code>
          </div>
          {!status?.rsync.modern && (
            <div className="notice compact">
              <AlertCircle size={16} />
              系统 openrsync 支持基础备份。需要扩展属性时，请安装 rsync 3.x
              并设置路径。
            </div>
          )}
          <label className="field">
            变化检测周期（秒）
            <input
              type="number"
              min={10}
              max={3600}
              value={cfg.pollSeconds}
              onChange={(e) =>
                setCfg({ ...cfg, pollSeconds: Number(e.target.value) })
              }
            />
            <small>
              原生监听异常时的轮询周期；静默等待在各计划中单独配置。
            </small>
          </label>
          <button
            className="btn primary"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void api("settings", "PATCH", cfg)
                .then(async () => {
                  notify("设置已保存");
                  await refresh();
                })
                .catch((e) => notify(e.message, true))
                .finally(() => setBusy(false));
            }}
          >
            {busy ? (
              <LoaderCircle size={16} className="spin" />
            ) : (
              <Check size={16} />
            )}
            保存设置
          </button>
        </div>
      </div>
      <div className="settings-box">
        <div className="setting-row">
          <div className="setting-icon">
            <RefreshCw size={19} />
          </div>
          <div>
            <b>从备份盘恢复配置与历史</b>
            <p>
              选择包含 LocalBackup
              仓库的目标目录，先校验并预览可恢复的计划、规则、磁盘绑定与历史。恢复的计划默认关闭自动备份。
            </p>
          </div>
          <button
            className="btn secondary small"
            disabled={busy}
            onClick={() => setRecoveryPicker(true)}
          >
            选择备份目录
          </button>
        </div>
      </div>
      {recoveryPicker && (
        <DirectoryPicker
          onClose={() => setRecoveryPicker(false)}
          onChoose={(path) => {
            setRecoveryPicker(false);
            setBusy(true);
            void api<NonNullable<typeof recovery>>(
              "repositories/recovery/preview",
              "POST",
              { path },
            )
              .then((result) => {
                setRecovery(result);
                setRestorePlans(true);
              })
              .catch((e) => notify(e.message, true))
              .finally(() => setBusy(false));
          }}
        />
      )}
      {recovery && (
        <Modal
          title="恢复配置与历史预览"
          wide
          subtitle="所有文件已完成内容校验；确认后才写入本地记录。"
          onClose={() => {
            if (!busy) setRecovery(null);
          }}
        >
          <p>
            发现 {recovery.snapshots} 份恢复点，其中 {recovery.pinned}{" "}
            份按永久保留导入。
          </p>
          <label className="field">
            <span>
              <input
                type="checkbox"
                checked={restorePlans}
                onChange={(e) => setRestorePlans(e.target.checked)}
                disabled={busy || !recovery.plans.length}
              />{" "}
              恢复 {recovery.plans.length} 个缺失计划，全部关闭自动备份
            </span>
          </label>
          {recovery.plans.map((p) => (
            <div className="notice compact recovery-plan" key={p.id}>
              <span>
                <b>{p.name}</b> · {p.sources.length} 个源目录 ·{" "}
                {p.destinations.length} 个绑定目标
                <br />
                恢复后请先检查目录与磁盘，再启用自动备份。
              </span>
              <details>
                <summary>查看目录、规则与调度</summary>
                <p>
                  {modeLabels[p.mode]} · 最近 {p.retention.keepLast} 份 /{" "}
                  {p.retention.keepDays || "不限"} 天；分层代表版本：每日{" "}
                  {p.retention.keepDaily || 0} / 每周{" "}
                  {p.retention.keepWeekly || 0} / 每月{" "}
                  {p.retention.keepMonthly || 0}（UTC）
                </p>
                {p.sources.map((src) => (
                  <p key={src.id}>
                    {src.name}：{src.path} → {src.targetPath}
                    {src.enabled ? "" : "（已停用）"}
                  </p>
                ))}
                {p.destinations.map((dest) => (
                  <p key={dest.id}>
                    {dest.name}：{dest.path}
                    <br />卷 UUID：{dest.volumeId}
                    {dest.enabled ? "" : "（已停用）"}
                  </p>
                ))}
                <p>
                  定时：
                  {p.schedule.intervalMinutes
                    ? `每 ${p.schedule.intervalMinutes} 分钟`
                    : "关闭"}{" "}
                  · 磁盘连接：{p.schedule.onMount ? "开启" : "关闭"} ·
                  文件变化：{p.schedule.onChange ? "开启" : "关闭"}
                </p>
                <p>计划文件条件：{recoveryFilterText(p.fileFilters)}</p>
                {p.sources
                  .filter((src) => src.fileFilters)
                  .map((src) => (
                    <p key={src.id}>
                      {src.name} 文件条件：{recoveryFilterText(src.fileFilters)}
                    </p>
                  ))}
                <p>
                  计划规则：
                  {(p.rules ?? [])
                    .map(
                      (rule) =>
                        `${rule.action === "include" ? "包含" : "排除"} ${rule.pattern}`,
                    )
                    .join("；") || "无"}
                </p>
                {p.sources.map((src) => (
                  <p key={src.id}>
                    {src.name} 规则：
                    {(src.rules ?? [])
                      .map(
                        (rule) =>
                          `${rule.action === "include" ? "包含" : "排除"} ${rule.pattern}`,
                      )
                      .join("；") || "沿用计划规则"}
                  </p>
                ))}
              </details>
            </div>
          ))}
          {recovery.warnings.map((message, i) => (
            <p className="notice compact" key={i}>
              {message}
            </p>
          ))}
          <div className="modal-footer">
            <button
              className="btn secondary"
              disabled={busy}
              onClick={() => setRecovery(null)}
            >
              取消
            </button>
            <button
              className="btn primary"
              disabled={busy}
              onClick={() => {
                setBusy(true);
                void api<{ imported: number; restoredPlans: number }>(
                  "repositories/recovery/apply",
                  "POST",
                  { previewId: recovery.previewId, restorePlans },
                )
                  .then(async (result) => {
                    notify(
                      `已恢复 ${result.restoredPlans} 个手动备份计划、${result.imported} 份历史记录`,
                    );
                    setRecovery(null);
                    await refresh();
                  })
                  .catch((e) => {
                    notify(e.message, true);
                    setRecovery(null);
                  })
                  .finally(() => setBusy(false));
              }}
            >
              {busy ? "正在重新校验并恢复…" : "确认恢复"}
            </button>
          </div>
        </Modal>
      )}
      <details className="advanced-diagnostics">
        <summary>
          高级诊断<span>变化监听与文件索引</span>
        </summary>
        <div className="settings-box">
          <div className="settings-title">
            <span className="setting-icon">
              <Activity size={19} />
            </span>
            <div>
              <h3>智能变化检测</h3>
              <p>
                {status?.watchStatus?.backend === "fsevents" &&
                status.watchStatus.healthy
                  ? status.watchStatus.roots
                    ? "macOS FSEvents 正在监听，空闲时无需遍历目录。"
                    : "macOS FSEvents 已就绪，尚无启用变化监听的计划。"
                  : "使用轮询扫描作为变化检测后备。"}
              </p>
            </div>
            <Badge kind={status?.watchStatus?.healthy ? "success" : "failed"}>
              {status?.watchStatus?.healthy ? "运行正常" : "后备扫描"}
            </Badge>
          </div>
          <div className="about-row">
            <span>监听目录</span>
            <span>
              {status?.watchStatus?.roots || 0} 个源目录（相同路径共享监听）
            </span>
          </div>
          <div className="about-row">
            <span>待处理变更</span>
            <span>{status?.watchStatus?.pending || 0} 条已持久化路径</span>
          </div>
          <div className="about-row">
            <span>事件游标</span>
            <code>{status?.watchStatus?.cursor || "等待事件"}</code>
          </div>
          {status?.watchStatus?.error && (
            <div className="notice error">{status.watchStatus.error}</div>
          )}
          <div className="notice compact">
            <ShieldCheck size={16} />
            事件丢失或服务重启后会校验完整内容，另有每小时安全审计。备份过程中新发生的变更会留待下一次处理。
          </div>
        </div>
        <IndexDiagnostics
          plans={plans}
          active={!!status?.activeRunId}
          notify={notify}
        />
      </details>
      <div className="settings-box about-box">
        <div className="settings-title">
          <Logo />
          <div>
            <h3>Foldsnap</h3>
            <p>本地优先的 macOS 增量备份工具 · v{status?.version}</p>
          </div>
        </div>
        {[
          ["数据存储", status?.database || "SQLite"],
          ["连接方式", "127.0.0.1 · 访问令牌认证 · 来源校验"],
          ["内容完整性", "SHA-256 校验 · 暂存后原子发布"],
          ["恢复方式", "恢复到新目录 · 不覆盖现有文件"],
        ].map(([label, value]) => (
          <div className="about-row" key={label}>
            <span>{label}</span>
            <span>{value}</span>
          </div>
        ))}
        <div className="notice compact">
          <Activity size={16} />
          服务运行时执行调度。睡眠或磁盘离线期间不保证准点执行，恢复后重新评估。
        </div>
      </div>
    </>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
