import { type ComponentProps, type ReactNode, useId, useSyncExternalStore } from "react";
import { DataTable } from "../../components/data-table";
import "./management-table.css";

type ManagementTableProps<T> = ComponentProps<typeof DataTable<T>> & {
  /** Record identifier announced by the mobile card's group name. */
  rowLabel?: (row: T) => ReactNode;
};

const narrowQuery = "(max-width: 719px)";
function subscribe(onChange: () => void) {
  const media = window.matchMedia?.(narrowQuery);
  media?.addEventListener("change", onChange);
  return () => media?.removeEventListener("change", onChange);
}
const isNarrow = () => window.matchMedia?.(narrowQuery).matches ?? false;
const serverSnapshot = () => false;

// Render a single set of controls so dialogs, pending actions and tab order stay unambiguous.
export function ManagementTable<T>({ rowLabel, ...props }: ManagementTableProps<T>) {
  const narrow = useSyncExternalStore(subscribe, isNarrow, serverSnapshot);
  const id = useId();
  if (!narrow || props.loading || props.error || !props.rows.length)
    return <DataTable {...props} />;
  return (
    <section className="management-records" aria-label={props.caption}>
      <h3>{props.caption}</h3>
      {props.rows.map((row, index) => {
        const label = rowLabel?.(row);
        const labelId =
          label == null || label === false || label === "" ? undefined : `${id}-${index}`;
        return (
          <fieldset key={props.rowKey(row)} className="min-w-0" aria-labelledby={labelId}>
            {labelId && (
              <span id={labelId} className="visually-hidden">
                {label}
              </span>
            )}
            <dl className="management-record">
              {props.columns.map((column) => (
                <div className="management-record-field" key={column.key}>
                  <dt>{column.header}</dt>
                  <dd>{column.cell(row)}</dd>
                </div>
              ))}
            </dl>
          </fieldset>
        );
      })}
    </section>
  );
}
