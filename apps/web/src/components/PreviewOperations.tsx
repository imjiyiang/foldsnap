import { useMemo, useState } from "react";
import { ArrowLeft, ArrowRight, Search } from "lucide-react";
import type { Operation, Plan } from "../types";
import { bytes } from "../types";

const labels: Record<string, string> = {
  added: "新增",
  modified: "修改",
  moved: "移动",
  deleted: "移除",
  retained: "保留",
  replaced: "类型变化",
};

export default function PreviewOperations({
  operations,
  plan,
  destructive = false,
}: {
  operations: Operation[];
  plan: Plan;
  destructive?: boolean;
}) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const filtered = useMemo(() => {
    const term = query.toLowerCase();
    return term
      ? operations.filter((o) =>
          (o.path + " " + (o.oldPath || "")).toLowerCase().includes(term),
        )
      : operations;
  }, [operations, query]);
  const pages = Math.max(1, Math.ceil(filtered.length / 100));
  const visiblePage = Math.min(page, pages - 1);
  return (
    <section
      className={"preview-operations " + (destructive ? "destructive" : "")}
    >
      <div className="operation-heading">
        <b>
          {destructive ? "将从同步视图移除" : "变更与保留"}
          <span>{operations.length} 项</span>
        </b>
        <div className="search">
          <Search size={14} />
          <input
            aria-label={destructive ? "搜索移除路径" : "搜索变更路径"}
            placeholder="搜索路径…"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setPage(0);
            }}
          />
        </div>
      </div>
      <div className="operation-list">
        {filtered.length ? (
          filtered
            .slice(visiblePage * 100, (visiblePage + 1) * 100)
            .map((o, i) => (
              <div key={i}>
                <span className={"op-label " + o.type}>
                  {labels[o.type] || o.type}
                </span>
                <span title={o.path}>
                  {o.oldPath && <small>{o.oldPath} → </small>}
                  {o.path}
                  <small>
                    {plan.sources.find((s) => s.id === o.sourceId)?.name ||
                      o.sourceId}{" "}
                    →{" "}
                    {
                      plan.destinations.find((d) => d.id === o.destinationId)
                        ?.name
                    }
                  </small>
                  {o.reason && <small>{o.reason}</small>}
                </span>
                <span>{bytes(o.size)}</span>
              </div>
            ))
        ) : (
          <div className="quiet-empty">
            {query ? "没有匹配的路径" : "没有新增、修改或保留项。"}
          </div>
        )}
      </div>
      <div className="operation-pagination">
        <small>
          {filtered.length
            ? `显示 ${visiblePage * 100 + 1}—${Math.min((visiblePage + 1) * 100, filtered.length)}`
            : "0 项"}{" "}
          / 共 {filtered.length} 项
        </small>
        <button
          className="icon-btn"
          aria-label={destructive ? "移除列表上一页" : "变更列表上一页"}
          disabled={visiblePage === 0}
          onClick={() => setPage(visiblePage - 1)}
        >
          <ArrowLeft size={15} />
        </button>
        <span>
          {visiblePage + 1} / {pages}
        </span>
        <button
          className="icon-btn"
          aria-label={destructive ? "移除列表下一页" : "变更列表下一页"}
          disabled={visiblePage + 1 >= pages}
          onClick={() => setPage(visiblePage + 1)}
        >
          <ArrowRight size={15} />
        </button>
      </div>
    </section>
  );
}
