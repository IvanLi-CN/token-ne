import {
  type ButtonHTMLAttributes,
  forwardRef,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
export const Button = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: "primary" | "secondary" | "danger";
    /** Icon-only action: no padding box, hover ring only. */
    ghost?: boolean;
  }
>(function Button(
  { variant = "primary", ghost = false, className = "", type = "button", ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={`button button-${variant}${ghost ? " button-ghost" : ""} ${className}`}
      {...props}
    />
  );
});
/** The one toggle control of the console: a switch, never a checkbox. */
export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      className={`switch${checked ? " is-on" : ""}`}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span className="switch-thumb" aria-hidden="true" />
    </button>
  );
}
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className = "", ...props }, ref) {
    return <input ref={ref} className={`input ${className}`} {...props} />;
  },
);
export function StatusBadge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "success" | "warning" | "error" | "neutral";
}) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}
export function Spinner({ label = "加载中" }: { label?: string }) {
  return (
    <span className="loading" role="status">
      <span className="spinner" aria-hidden="true" />
      {label}
    </span>
  );
}
export function EmptyState({
  title,
  children,
  action,
}: {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <h2>{title}</h2>
      <p>{children}</p>
      {action}
    </div>
  );
}
export function ErrorState({
  title = "加载失败",
  message,
  onRetry,
}: {
  title?: string;
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="error-state" role="alert">
      <h2>{title}</h2>
      <p>{message}</p>
      {onRetry && (
        <Button variant="secondary" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  );
}
const sectionLabels: Record<string, string> = {
  总览: "总览",
  请求: "请求",
  Models: "Models",
  Upstreams: "Upstreams",
  "Downstream Keys": "Downstream Keys",
  Cores: "Cores",
  系统设置: "系统设置",
};
export function PageTitle({ title }: { title: string }) {
  return (
    <h1 id="page-title" tabIndex={-1} className="visually-hidden">
      {title}
    </h1>
  );
}
export function PageHeader({
  title,
  description,
  actions,
  eyebrow,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
  eyebrow?: string;
}) {
  return (
    <header className="page-header">
      <div className="page-heading">
        <span className="page-eyebrow">{eyebrow ?? sectionLabels[title] ?? "系统"}</span>
        <div className="page-title-row">
          <h1 tabIndex={-1} id="page-title">
            {title}
          </h1>
        </div>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}
