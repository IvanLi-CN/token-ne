import { type ComponentProps, useSyncExternalStore } from "react";
import { DataTable } from "../../components/data-table";
import "./management-table.css";

const narrowQuery = "(max-width: 719px)";
function subscribe(onChange: () => void) {
  const media = window.matchMedia?.(narrowQuery);
  media?.addEventListener("change", onChange);
  return () => media?.removeEventListener("change", onChange);
}
const isNarrow = () => window.matchMedia?.(narrowQuery).matches ?? false;
const serverSnapshot = () => false;

// Render a single set of controls so dialogs, pending actions and tab order stay unambiguous.
export function ManagementTable<T>(props: ComponentProps<typeof DataTable<T>>) {
  const narrow = useSyncExternalStore(subscribe, isNarrow, serverSnapshot);
  if (!narrow || props.loading || props.error || !props.rows.length)
    return <DataTable {...props} />;
  return (
    <section className="management-records" aria-label={props.caption}>
      <h3>{props.caption}</h3>
      {props.rows.map((row) => (
        <dl className="management-record" key={props.rowKey(row)}>
          {props.columns.map((column) => (
            <div className="management-record-field" key={column.key}>
              <dt>{column.header}</dt>
              <dd>{column.cell(row)}</dd>
            </div>
          ))}
        </dl>
      ))}
    </section>
  );
}
