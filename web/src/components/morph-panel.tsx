import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";

const MorphContext = createContext<{
  openId: string | null;
  setOpenId: (id: string | null) => void;
} | null>(null);
export function MorphGroup({ children }: { children: ReactNode }) {
  const [openId, setOpenId] = useState<string | null>(null);
  return (
    <MorphContext.Provider value={{ openId, setOpenId }}>
      <div className="morph-grid">{children}</div>
    </MorphContext.Provider>
  );
}
export function MorphPanel({
  title,
  summary,
  detail,
  label = "查看说明",
  className = "",
}: {
  title: string;
  summary: ReactNode;
  detail: ReactNode;
  label?: string;
  className?: string;
}) {
  const context = useContext(MorphContext);
  if (!context) throw new Error("MorphPanel must be inside MorphGroup");
  const generated = useId();
  const id = `morph-${generated}`;
  const triggerRef = useRef<HTMLButtonElement>(null);
  const open = context.openId === id;
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        context.setOpenId(null);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, context]);
  return (
    <section className={`morph-panel ${open ? "is-expanded" : ""} ${className}`}>
      <button
        ref={triggerRef}
        type="button"
        className="morph-trigger"
        aria-expanded={open}
        aria-controls={`${id}-detail`}
        onClick={() => context.setOpenId(open ? null : id)}
      >
        <span className="morph-title">{title}</span>
        <span className="morph-summary">{summary}</span>
        <span className="morph-action">{open ? "收起" : label}</span>
      </button>
      <div id={`${id}-detail`} className="morph-detail" aria-hidden={!open} inert={!open}>
        {detail}
      </div>
    </section>
  );
}
