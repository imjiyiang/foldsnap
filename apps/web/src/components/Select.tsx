import {
  Children,
  isValidElement,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type CSSProperties,
  type SelectHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import { Check, ChevronDown } from "lucide-react";

type Props = Omit<
  SelectHTMLAttributes<HTMLSelectElement>,
  "onChange" | "value" | "multiple" | "size"
> & {
  value: string | number;
  onChange?: (event: { target: { value: string } }) => void;
};

// All existing single-choice fields share this keyboard-accessible listbox.
// The portal avoids clipping inside scrolling drawers and restore dialogs.
export default function Select({
  value,
  onChange,
  children,
  disabled,
  className = "",
  id: suppliedId,
  "aria-label": label,
  "aria-describedby": describedBy,
}: Props) {
  const uid = useId(),
    id = suppliedId ?? uid;
  const trigger = useRef<HTMLButtonElement>(null),
    menu = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false),
    [active, setActive] = useState(0);
  const [position, setPosition] = useState<CSSProperties>({
    left: 0,
    top: 0,
    width: 0,
    maxHeight: 280,
  });
  const typed = useRef({ text: "", at: 0 });
  const options = Children.toArray(children).flatMap((child) => {
    if (
      !isValidElement<{
        value?: string | number;
        children?: ReactNode;
        disabled?: boolean;
      }>(child)
    )
      return [];
    const text = Children.toArray(child.props.children).join("");
    return [
      {
        value: String(child.props.value ?? text),
        text,
        disabled: !!child.props.disabled,
      },
    ];
  });
  const selected = options.findIndex((o) => o.value === String(value));
  const choose = (index: number) => {
    const option = options[index];
    if (!option || option.disabled) return;
    if (option.value !== String(value))
      onChange?.({ target: { value: option.value } });
    setOpen(false);
    trigger.current?.focus();
  };
  const show = (last = false) => {
    if (disabled || !options.length) return;
    typed.current = { text: "", at: 0 };
    setActive(selected >= 0 ? selected : last ? options.length - 1 : 0);
    setOpen(true);
  };
  useLayoutEffect(() => {
    if (!open || !trigger.current) return;
    const r = trigger.current.getBoundingClientRect();
    const width = Math.min(Math.max(r.width, 200), window.innerWidth - 24);
    const below = window.innerHeight - r.bottom - 12,
      above = r.top - 12;
    const up = below < Math.min(280, options.length * 40 + 12) && above > below;
    const maxHeight = Math.max(40, Math.min(280, up ? above - 6 : below - 6));
    setPosition({
      left: Math.max(12, Math.min(r.left, window.innerWidth - width - 12)),
      top: up ? undefined : r.bottom + 6,
      bottom: up ? window.innerHeight - r.top + 6 : undefined,
      width,
      maxHeight,
    });
  }, [open, options.length]);
  useEffect(() => {
    if (open)
      menu.current
        ?.querySelector<HTMLElement>(`[data-index="${active}"]`)
        ?.scrollIntoView({ block: "nearest" });
  }, [open, active]);
  useEffect(() => {
    if (!open) return;
    const outside = (e: PointerEvent) => {
      if (
        !trigger.current?.contains(e.target as Node) &&
        !menu.current?.contains(e.target as Node)
      )
        setOpen(false);
    };
    const scroll = (e: Event) => {
      if (!menu.current?.contains(e.target as Node)) setOpen(false);
    };
    const close = () => setOpen(false);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", scroll, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", scroll, true);
    };
  }, [open]);
  return (
    <>
      <button
        ref={trigger}
        id={id}
        type="button"
        role="combobox"
        className={`select-trigger ${className}`}
        disabled={disabled || !options.length}
        aria-label={label}
        aria-describedby={describedBy}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? `${id}-list` : undefined}
        aria-activedescendant={open ? `${id}-option-${active}` : undefined}
        onClick={() => (open ? setOpen(false) : show())}
        onBlur={(e) => {
          if (!menu.current?.contains(e.relatedTarget)) setOpen(false);
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape" && open) {
            e.preventDefault();
            e.stopPropagation();
            setOpen(false);
            return;
          }
          if (e.key === "Tab") {
            setOpen(false);
            return;
          }
          if (["Enter", " "].includes(e.key)) {
            e.preventDefault();
            open ? choose(active) : show();
            return;
          }
          if (["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
            e.preventDefault();
            if (!open) {
              show(e.key === "ArrowUp" || e.key === "End");
              return;
            }
            const direction = e.key === "ArrowUp" || e.key === "End" ? -1 : 1;
            let next =
              e.key === "Home"
                ? 0
                : e.key === "End"
                  ? options.length - 1
                  : (active + direction + options.length) % options.length;
            for (let i = 0; i < options.length && options[next].disabled; i++)
              next = (next + direction + options.length) % options.length;
            setActive(next);
            return;
          }
          if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
            e.preventDefault();
            typed.current = {
              text:
                (Date.now() - typed.current.at < 700
                  ? typed.current.text
                  : "") + e.key.toLowerCase(),
              at: Date.now(),
            };
            const next = options.findIndex(
              (o) =>
                !o.disabled &&
                o.text.toLowerCase().startsWith(typed.current.text),
            );
            if (next >= 0) {
              if (!open) setOpen(true);
              setActive(next);
            }
          }
        }}
      >
        <span>{options[selected]?.text ?? "请选择"}</span>
        <ChevronDown size={16} />
      </button>
      {open &&
        createPortal(
          <div
            ref={menu}
            id={`${id}-list`}
            role="listbox"
            aria-label={label ?? "可选项"}
            className="select-menu"
            style={position}
          >
            {options.map((option, index) => (
              <div
                key={option.value}
                id={`${id}-option-${index}`}
                role="option"
                aria-selected={selected === index}
                aria-disabled={option.disabled || undefined}
                data-index={index}
                className={`select-option ${index === active ? "active" : ""} ${selected === index ? "chosen" : ""}`}
                onPointerMove={() => {
                  if (!option.disabled) setActive(index);
                }}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(index)}
              >
                <span>{option.text}</span>
                {selected === index && <Check size={16} />}
              </div>
            ))}
          </div>,
          trigger.current?.closest('[role="dialog"]') ?? document.body,
        )}
    </>
  );
}
