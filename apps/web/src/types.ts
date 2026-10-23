export type ScanStats = {
  fullSources: number;
  subtreeSources: number;
  pathsVisited: number;
  projectedPaths: number;
  filesHashed: number;
  cacheHits: number;
  bytesHashed: number;
  bytesReused: number;
  forced: boolean;
};
export type FileException = { path: string; action: "include" | "exclude" };
export type FileFilters = {
  minBytes?: number;
  maxBytes?: number;
  modifiedWithinDays?: number;
  types?: string[];
  exceptions?: FileException[];
};
export type Rule = { action: "exclude" | "include"; pattern: string };
export type Source = {
  volumeId?: string;
  directoryId?: string;
  directoryBirthTime?: string;
  mountPath?: string;
  volumeRelativePath?: string;
  fileFilters?: FileFilters;
  id: string;
  name: string;
  path: string;
  targetPath: string;
  enabled: boolean;
  excludeHidden: boolean;
  rules: Rule[];
};
export type Destination = {
  mountPath?: string;
  volumeRelativePath?: string;
  id: string;
  name: string;
  path: string;
  volumeId: string;
  enabled: boolean;
};
export type Plan = {
  fileFilters?: FileFilters;
  id: string;
  name: string;
  description: string;
  mode: string;
  enabled: boolean;
  sources: Source[];
  destinations: Destination[];
  rules: Rule[];
  schedule: {
    minChangedFiles?: number;
    minChangedBytes?: number;
    minIntervalMinutes?: number;
    maxIntervalMinutes?: number;
    maxAttempts?: number;
    intervalMinutes: number;
    onMount: boolean;
    onChange: boolean;
    debounceSeconds: number;
  };
  retention: {
    keepLast: number;
    keepDays: number;
    keepDaily?: number;
    keepWeekly?: number;
    keepMonthly?: number;
  };
  createdAt?: string;
  updatedAt?: string;
};
export type Operation = {
  sourceId: string;
  destinationId: string;
  path: string;
  oldPath?: string;
  type: string;
  size: number;
  reason?: string;
};
export type Preview = {
  sourceIds?: string[];
  scanStats?: ScanStats;
  removals: Operation[];
  previewId?: string;
  expiresAt?: string;
  deletePaths: number;
  deleteBytes: number;
  retained: number;
  retainedBytes: number;
  operations: Operation[];
  files: number;
  bytes: number;
  transferBytes: number;
  added: number;
  modified: number;
  moved: number;
  deleted: number;
  reused: number;
  warnings: string[];
};
export type Run = {
  noChanges?: boolean;
  createdSnapshotIds?: string[];
  unchangedDestinationIds?: string[];
  approvedPreviewId?: string;
  automaticAttempt?: number;
  retryOf?: string;
  sourceIds?: string[];
  sourceNames?: string[];
  destinationIds?: string[];
  queuedAt?: string;
  id: string;
  planId: string;
  planName: string;
  status: string;
  stage: string;
  progress: number;
  startedAt: string;
  finishedAt?: string;
  error?: string;
  logs: string[];
  preview?: Preview;
  trigger: string;
};
export type Snapshot = {
  updatedSourceIds?: string[];
  mode?: string;
  currentPath?: string;
  id: string;
  planId: string;
  planName: string;
  destinationId: string;
  destinationName: string;
  destinationPath: string;
  createdAt: string;
  files: number;
  bytes: number;
  pinned: boolean;
  sources: Source[];
};
export type Entry = {
  sourceId: string;
  path: string;
  kind: string;
  size: number;
  mtime: number;
  mode: number;
  hash?: string;
  link?: string;
};
export type Volume = {
  bindings?: {
    planId: string;
    planName: string;
    destinationId: string;
    relativePath: string;
    enabled: boolean;
    automatic: boolean;
  }[];
  id: string;
  name: string;
  path: string;
  capacity: number;
  free: number;
  connected: boolean;
};
export type Status = {
  version: string;
  database: string;
  rsync: { path: string; version: string; available: boolean; modern: boolean };
  activeRunId: string;
  home: string;
  watcher: string;
  watchStatus?: {
    backend: string;
    healthy: boolean;
    roots: number;
    cursor: number;
    pending: number;
    lastEventAt: string;
    error?: string;
  };
};
export const bytes = (n: number) => {
  if (!n) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${["B", "KB", "MB", "GB", "TB"][i]}`;
};
export const date = (s: string) =>
  new Date(s).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });

export type PreviewJob = {
  previewId?: string;
  id: string;
  planId: string;
  planName: string;
  sourceIds?: string[];
  state: "queued" | "running" | "cancelling" | "cancelled" | "failed" | "ready";
  stage: string;
  createdAt: string;
  finishedAt?: string;
  pathsRead: number;
  bytesRead: number;
  currentPath?: string;
  error?: string;
  result?: Preview;
};
