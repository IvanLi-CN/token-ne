import { MorphGroup, MorphPanel } from "./morph-panel";
import { PageHeader, StatusBadge } from "./primitives";
import { ProximitySurface } from "./proximity-surface";
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
        actions={<StatusBadge tone="warning">待接入</StatusBadge>}
      />
      <ProximitySurface className="instrument-placeholder">
        <div className="placeholder-summary">
          <div>
            <span className="placeholder-label">数据源状态</span>
            <h2>尚无生产数据</h2>
          </div>
          <StatusBadge>待接入</StatusBadge>
        </div>
        <p>当前页面展示结构说明；接入真实接口后再呈现运行状态。</p>
        <MorphGroup>
          <MorphPanel
            className="placeholder-detail"
            title="数据约束"
            summary="没有可用的真实数据源。"
            label="了解详情"
            detail={
              <p>
                此处暂不显示示例业务指标、成功率、请求量或状态值。数据接入后会由对应接口填充，并保留加载、空数据和错误状态。
              </p>
            }
          />
        </MorphGroup>
      </ProximitySurface>
    </>
  );
}
