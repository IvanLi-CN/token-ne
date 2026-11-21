import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
export function Button({
  variant = "primary",
  className = "",
  type = "button",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "danger" }) {
  return <button type={type} className={`button button-${variant} ${className}`} {...props} />;
}
export function Input({ className = "", ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={`input ${className}`} {...props} />;
}
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
};
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
          {actions}
        </div>
        {description && <p>{description}</p>}
      </div>
      <div className="header-rule" aria-hidden="true" />
    </header>
  );
}
