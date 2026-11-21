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
        <EmptyState title="尚无数据源">数据接入后将在此显示状态与操作。</EmptyState>
        <span className="placeholder-coordinate">数据源未连接</span>
      </section>
    </>
  );
}
