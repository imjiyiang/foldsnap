import { useEffect, useState } from "react";
import { AlertCircle, ShieldCheck, LoaderCircle } from "lucide-react";
import { api } from "../api";
import type { Source } from "../types";

type Inspection = { state: string; message: string; source?: Source };
export function SourceBinding({ source }: { source: Source }) {
  const [inspection, setInspection] = useState<Inspection | null>(null);
  useEffect(() => {
    let current = true;
    setInspection(null);
    void api<Inspection>("system/source/inspect", "POST", source)
      .then((value) => {
        if (current) setInspection(value);
      })
      .catch((error) => {
        if (current)
          setInspection({
            state: "unavailable",
            message: (error as Error).message,
          });
      });
    return () => {
      current = false;
    };
  }, [
    source.path,
    source.volumeId,
    source.directoryId,
    source.directoryBirthTime,
  ]);
  const warning =
    inspection?.state === "changed" || inspection?.state === "unavailable";
  return (
    <div className={"source-binding " + (warning ? "warning" : "")}>
      {!inspection ? (
        <LoaderCircle size={14} className="spin" />
      ) : warning ? (
        <AlertCircle size={14} />
      ) : (
        <ShieldCheck size={14} />
      )}
      <div>
        <b>{inspection?.message || "正在核对源目录身份…"}</b>
        {(source.volumeId || inspection?.source?.volumeId) && (
          <small>
            卷 UUID{" "}
            <code>{source.volumeId || inspection?.source?.volumeId}</code>
            <br />
            目录 ID{" "}
            <code>{source.directoryId || inspection?.source?.directoryId}</code>
          </small>
        )}
        {inspection?.source?.path && inspection.source.path !== source.path && (
          <small>当前挂载路径：{inspection.source.path}</small>
        )}
        <small>目录改名后点击路径重新选择；不同目录请作为新源添加。</small>
      </div>
    </div>
  );
}
