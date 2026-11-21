import { MorphGroup, MorphPanel } from "../../components/morph-panel";
import { PageHeader, StatusBadge } from "../../components/primitives";
import { ProximitySurface } from "../../components/proximity-surface";

const indicators = [
  {
    title: "请求量",
    summary: "等待数据源",
    detail: "趋势图将在使用 overview API 接入后显示。当前没有样本请求可供绘制。",
  },
  {
    title: "成功率",
    summary: "未计算",
    detail: "成功率只会从真实请求记录计算；此处没有模拟百分比。",
  },
  { title: "Token 用量", summary: "未统计", detail: "用量接入后会按所选时间范围展示真实聚合。" },
  {
    title: "费用",
    summary: "未统计",
    detail: "费用依据模型计费配置和请求用量计算，不以零值占位。",
  },
  {
    title: "P95 延迟",
    summary: "未计算",
    detail: "延迟分位值需要真实请求样本；当前没有可用分布。",
  },
  {
    title: "P95 TTFT",
    summary: "未计算",
    detail: "首 token 延迟分位值将在 usage 数据源接入后提供。",
  },
];
export default function OverviewPage() {
  return (
    <>
      <PageHeader
        title="总览"
        description="请求、用量、延迟与核心运行状态。"
        actions={<StatusBadge tone="warning">待接入</StatusBadge>}
      />
      <ProximitySurface className="overview-status">
        <span className="placeholder-label">数据状态</span>
        <strong>尚无生产数据</strong>
        <span className="overview-note">待 overview 与 usage 查询接口接入后显示</span>
      </ProximitySurface>
      <MorphGroup>
        {indicators.map((item) => (
          <MorphPanel
            key={item.title}
            title={item.title}
            summary={<span className="metric-pending">{item.summary}</span>}
            label="查看说明"
            detail={
              <>
                <p>{item.detail}</p>
                <div
                  className="chart-placeholder"
                  role="img"
                  aria-label={`${item.title}图表待接入`}
                >
                  <span>图表待接入</span>
                  <span aria-hidden="true">—</span>
                </div>
              </>
            }
          />
        ))}
      </MorphGroup>
      <p className="sample-disclaimer">指标为数据接入位置，不含示例或模拟业务数值。</p>
    </>
  );
}
