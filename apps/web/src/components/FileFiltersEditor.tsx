import Select from "./Select";
import { useState } from "react";
import { Plus, X } from "lucide-react";
import type { FileFilters } from "../types";
const units = { B: 1, KB: 1024, MB: 1024 ** 2, GB: 1024 ** 3 };
function SizeLimit({
  label,
  value,
  onChange,
}: {
  label: string;
  value?: number;
  onChange: (v: number | undefined) => void;
}) {
  const [unit, setUnit] = useState<keyof typeof units>(() =>
    value == null
      ? "MB"
      : value >= units.GB && value % units.GB === 0
        ? "GB"
        : value >= units.MB && value % units.MB === 0
          ? "MB"
          : value >= units.KB && value % units.KB === 0
            ? "KB"
            : "B",
  );
  return (
    <label className="file-condition">
      <span>{label}</span>
      <div className="size-limit">
        <input
          aria-label={label}
          type="number"
          min="0"
          step="any"
          max={Number.MAX_SAFE_INTEGER / units[unit]}
          placeholder="不限"
          value={value == null ? "" : value / units[unit]}
          onChange={(e) =>
            onChange(
              e.target.value === ""
                ? undefined
                : Math.round(Number(e.target.value) * units[unit]),
            )
          }
        />
        <Select
          aria-label={label + "单位"}
          value={unit}
          onChange={(e) => setUnit(e.target.value as keyof typeof units)}
        >
          {Object.keys(units).map((u) => (
            <option key={u}>{u}</option>
          ))}
        </Select>
      </div>
    </label>
  );
}
export default function FileFiltersEditor({
  value = {},
  onChange,
}: {
  value?: FileFilters;
  onChange: (v: FileFilters) => void;
}) {
  const patch = (p: Partial<FileFilters>) => onChange({ ...value, ...p });
  const exceptions = value.exceptions ?? [];
  return (
    <div className="file-filters-editor">
      <div className="file-conditions">
        <SizeLimit
          label="最小文件大小（含）"
          value={value.minBytes}
          onChange={(minBytes) => patch({ minBytes })}
        />
        <SizeLimit
          label="最大文件大小（含）"
          value={value.maxBytes}
          onChange={(maxBytes) => patch({ maxBytes })}
        />
        <label className="file-condition">
          <span>修改时间 · 最近 N 天</span>
          <input
            aria-label="最近修改天数"
            type="number"
            min="1"
            max="36500"
            step="1"
            placeholder="不限"
            value={value.modifiedWithinDays ?? ""}
            onChange={(e) =>
              patch({
                modifiedWithinDays:
                  e.target.value === "" ? undefined : Number(e.target.value),
              })
            }
          />
        </label>
      </div>
      <label className="field-label">
        文件类型 <span>不选表示全部；按扩展名识别</span>
      </label>
      <div className="file-type-options">
        {[
          ["image", "图片"],
          ["video", "视频"],
          ["audio", "音频"],
          ["document", "文档"],
          ["archive", "归档 / 镜像"],
          ["code", "代码"],
          ["other", "其他 / 无扩展名"],
        ].map(([id, label]) => (
          <label key={id}>
            <input
              type="checkbox"
              checked={value.types?.includes(id) ?? false}
              onChange={(e) =>
                patch({
                  types: e.target.checked
                    ? [...(value.types ?? []), id]
                    : (value.types ?? []).filter((t) => t !== id),
                })
              }
            />
            {label}
          </label>
        ))}
      </div>
      <p className="muted filter-help">
        文件条件仅作用于普通文件；目录用于保留结构，符号链接按路径规则处理且不跟随目标。大小、时间和类型条件需同时满足。
      </p>
      <div className="exception-heading">
        <b>单文件例外</b>
        <span>优先于路径规则、隐藏文件和文件条件</span>
      </div>
      {exceptions.map((rule, i) => (
        <div className="rule-edit" key={i}>
          <Select
            aria-label="单文件例外操作"
            value={rule.action}
            onChange={(e) =>
              patch({
                exceptions: exceptions.map((r, j) =>
                  i === j
                    ? { ...r, action: e.target.value as "include" | "exclude" }
                    : r,
                ),
              })
            }
          >
            <option value="include">强制包含</option>
            <option value="exclude">强制排除</option>
          </Select>
          <input
            aria-label="单文件例外路径"
            placeholder="源目录相对路径，如 cache/重要文档.pdf"
            value={rule.path}
            onChange={(e) =>
              patch({
                exceptions: exceptions.map((r, j) =>
                  i === j ? { ...r, path: e.target.value } : r,
                ),
              })
            }
          />
          <button
            type="button"
            className="icon-btn"
            aria-label="删除单文件例外"
            onClick={() =>
              patch({ exceptions: exceptions.filter((_, j) => j !== i) })
            }
          >
            <X size={15} />
          </button>
        </div>
      ))}
      <button
        type="button"
        className="text-btn"
        onClick={() =>
          patch({
            exceptions: [...exceptions, { path: "", action: "include" }],
          })
        }
      >
        <Plus size={14} />
        添加单文件例外
      </button>
      <p className="muted filter-help">
        填写完整相对文件名，不使用通配符。强制包含会保留必要的父目录，其他文件仍按原规则筛选。
      </p>
    </div>
  );
}
