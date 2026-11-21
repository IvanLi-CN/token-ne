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
        actions={<StatusBadge>页面待接入</StatusBadge>}
      />
      <section className="panel" aria-label={`${title}内容`}>
        <div className="panel-label">{scope}</div>
        <EmptyState title="此页面尚未接入业务数据">
          功能正在建设中；接入后将在此显示真实数据与操作。
        </EmptyState>
      </section>
    </>
  );
}
