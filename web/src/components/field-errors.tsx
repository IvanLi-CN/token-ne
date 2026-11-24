import { ApiError } from "../lib/api";

/**
 * 逐项展示管理 API 的字段错误。`details[].path` 是契约的一部分，
 * 冲突与校验失败都必须让管理员看到具体字段。
 */
export function FieldFailure({ error }: { error: unknown }) {
  if (!error) return null;
  const message =
    error instanceof ApiError
      ? error.message
      : error instanceof Error
        ? error.message
        : "操作失败，请重试。";
  const details = error instanceof ApiError ? error.details : [];
  const requestId = error instanceof ApiError ? error.requestId : "";
  return (
    <div className="field-error" role="alert">
      <p>{message}</p>
      {details.length > 0 && (
        <ul aria-label="字段错误">
          {details.map((detail) => (
            <li key={`${detail.path}:${detail.reason}`}>
              <code>{detail.path}</code>：{detail.reason}
            </li>
          ))}
        </ul>
      )}
      {requestId && <small className="muted">请求 ID：{requestId}</small>}
    </div>
  );
}

export function fieldInvalid(error: unknown, path: string): boolean {
  return (
    error instanceof ApiError &&
    error.details.some((detail) => detail.path === path || detail.path.startsWith(`${path}.`))
  );
}
