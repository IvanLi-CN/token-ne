import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { Button, EmptyState, ErrorState, Spinner } from "./primitives";
export interface Column<T> {
  key: string;
  header: ReactNode;
  cell: (row: T) => ReactNode;
  width?: string;
  minWidth?: string;
}
export function DataTable<T>({
  caption,
  captionHidden = false,
  columns,
  rows,
  rowKey,
  loading,
  error,
  onRetry,
  emptyTitle = "暂无数据",
  emptyDescription = "请调整筛选条件后重试。",
  nextCursor,
  onNext,
  onPrevious,
  rowAttrs,
}: {
  caption: string;
  /** Keep the accessible name but drop the visible title when a host heading already names it. */
  captionHidden?: boolean;
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  error?: string;
  onRetry?: () => void;
  emptyTitle?: string;
  emptyDescription?: string;
  nextCursor?: string | null;
  onNext?: (cursor: string) => void;
  onPrevious?: () => void;
  /**
   * Row-level attributes, e.g. native HTML5 drag handlers for an ordered list.
   * `key` and `children` are excluded: React reads `key` from a spread props
   * object after the explicit one, so allowing it would silently rekey rows and
   * carry stale cell state across reloads.
   */
  rowAttrs?: (row: T) => Omit<ComponentPropsWithoutRef<"tr">, "key" | "children"> | undefined;
}) {
  if (loading) return <Spinner label={`正在加载${caption}`} />;
  if (error) return <ErrorState message={error} onRetry={onRetry} />;
  if (!rows.length) return <EmptyState title={emptyTitle}>{emptyDescription}</EmptyState>;
  return (
    <>
      {/* biome-ignore lint/a11y/noNoninteractiveTabindex: keyboard users must scroll wide tables. */}
      <section className="table-scroll table-scroll-card" aria-label={caption} tabIndex={0}>
        <table>
          <caption className={captionHidden ? "visually-hidden" : undefined}>{caption}</caption>
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column.key} scope="col">
                  {column.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={rowKey(row)} {...rowAttrs?.(row)}>
                {columns.map((column) => (
                  <td
                    key={column.key}
                    style={
                      column.width || column.minWidth
                        ? { width: column.width, minWidth: column.minWidth }
                        : undefined
                    }
                  >
                    {column.cell(row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      {(onNext || onPrevious) && (
        <nav className="pagination" aria-label="表格分页">
          <Button variant="secondary" disabled={!onPrevious} onClick={onPrevious}>
            上一页
          </Button>
          <Button
            variant="secondary"
            disabled={!nextCursor || !onNext}
            onClick={() => {
              if (nextCursor) onNext?.(nextCursor);
            }}
          >
            下一页
          </Button>
        </nav>
      )}
    </>
  );
}
