import FingerprintStats from "./FingerprintStats";
import { type ScanStats } from "../types";
import { useState } from "react";
import { bytes, type Entry, type Operation, type Source } from "../types";
export type RulePreview = {
  scanStats?: ScanStats;
  entries: Entry[];
  ignored: Operation[];
  selectedReasons: Operation[];
  sources: Source[];
  evaluatedAt: string;
};
export default function RuleResults({ value }: { value: RulePreview }) {
  const [kind, setKind] = useState("selected"),
    [query, setQuery] = useState(""),
    [page, setPage] = useState(0);
  const list = kind === "selected" ? value.selectedReasons : value.ignored;
  const filtered = list.filter((r) =>
    (r.path + " " + r.reason)
      .toLocaleLowerCase()
      .includes(query.toLocaleLowerCase()),
  );
  const pages = Math.max(1, Math.ceil(filtered.length / 100));
  const current = Math.min(page, pages - 1);
  const names = new Map(value.sources.map((s) => [s.id, s.name]));
  return (
    <section className="rule-preview">
      <h4>
        {value.entries.filter((e) => e.kind !== "directory").length}{" "}
        个文件会备份 ·{" "}
        {bytes(
          value.entries
            .filter((e) => e.kind === "file")
            .reduce((sum, e) => sum + e.size, 0),
        )}
      </h4>
      <FingerprintStats value={value.scanStats} />
      <div className="rule-preview-controls">
        <button
          type="button"
          className={"btn small " + (kind === "selected" ? "secondary" : "")}
          onClick={() => {
            setKind("selected");
            setPage(0);
          }}
        >
          选中 {value.selectedReasons.length}
        </button>
        <button
          type="button"
          className={"btn small " + (kind === "ignored" ? "secondary" : "")}
          onClick={() => {
            setKind("ignored");
            setPage(0);
          }}
        >
          忽略 {value.ignored.length}
        </button>
        <input
          aria-label="搜索规则测试结果"
          placeholder="搜索路径或原因"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setPage(0);
          }}
        />
      </div>
      <div className="rule-decision-list">
        {filtered.slice(current * 100, (current + 1) * 100).map((r, i) => (
          <div key={r.sourceId + "/" + r.path + "/" + i}>
            <span>
              {names.get(r.sourceId)} / {r.path}
            </span>
            <small>{r.reason}</small>
          </div>
        ))}
        {!filtered.length && <p className="muted">没有匹配结果</p>}
      </div>
      <div className="rule-result-pagination">
        <span>
          共 {filtered.length} 项 · {current + 1} / {pages} 页
        </span>
        <button
          className="btn small"
          type="button"
          disabled={current === 0}
          onClick={() => setPage(current - 1)}
        >
          上一页
        </button>
        <button
          className="btn small"
          type="button"
          disabled={current + 1 === pages}
          onClick={() => setPage(current + 1)}
        >
          下一页
        </button>
      </div>
      <p className="muted filter-help">
        条件以 {new Date(value.evaluatedAt).toLocaleString("zh-CN")}{" "}
        计算。排除文件夹会跳过其子树，仅列出被跳过的文件夹；单文件包含所需的父目录会单独遍历。
      </p>
    </section>
  );
}
