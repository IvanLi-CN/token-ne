import { EmptyState, PageHeader, StatusBadge } from "./primitives";
export function PlaceholderPage({
  title,
  description,
  scope,
}: {
  title: string;
  description: string;
  scope: string;
}) {
  return (
    <>
      <PageHeader
        title={title}
        description={description}
        eyebrow={scope}
        actions={<StatusBadge>待接入</StatusBadge>}
      />
      <section className="instrument-placeholder" aria-label={`${title}内容`}>
        <span className="placeholder-crosshair" aria-hidden="true">
          +
        </span>
        <EmptyState title="尚无业务数据源">
          功能接入后，此观测面板将在此呈现实时状态与操作。
        </EmptyState>
        <span className="placeholder-coordinate">SIGNAL / NOT CONNECTED</span>
      </section>
    </>
  );
}
