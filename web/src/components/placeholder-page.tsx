import { Activity, ArrowUpRight, Database, Layers } from "lucide-react";
import { useState } from "react";
import { PageHeader, StatusBadge } from "./primitives";

const ranges = ["今天", "昨天", "近 24 小时"] as const;

export function PlaceholderPage({
  title,
  description,
  scope,
}: {
  title: string;
  description: string;
  scope: string;
}) {
  const [range, setRange] = useState<(typeof ranges)[number]>("今天");
  const overview = title === "总览";
  const stats = overview
    ? ["成功率", "Token 用量", "费用", "P95 延迟", "P95 TTFT"]
    : ["数据记录", "可用状态", "最近更新"];
  return (
    <>
      <PageHeader
        title={title}
        description={description}
        eyebrow={scope}
        actions={<StatusBadge tone="warning">数据待接入</StatusBadge>}
      />
      <section className="surface-card summary-panel" aria-labelledby="summary-heading">
        <div className="panel-heading">
          <h2 id="summary-heading">{overview ? "活动概览" : `${title} 概览`}</h2>
          <fieldset className="segmented-control range-control">
            <legend className="visually-hidden">时间范围</legend>
            {ranges.map((item) => (
              <button
                type="button"
                key={item}
                aria-pressed={range === item}
                onClick={() => setRange(item)}
              >
                {item}
              </button>
            ))}
          </fieldset>
        </div>
        <div className="summary-body">
          <div className="summary-readout">
            <span className="section-label">{overview ? "请求量" : "数据源状态"}</span>
            <strong className="summary-value">—</strong>
            <StatusBadge>尚无生产数据</StatusBadge>
            <p>{overview ? "等待 overview 与 usage 查询接口接入。" : "等待对应业务接口接入。"}</p>
          </div>
          <div className="activity-empty" aria-live="polite">
            <Activity size={28} strokeWidth={1.5} aria-hidden="true" />
            <h3>{range}暂无可展示的数据</h3>
            <p>接入真实数据后，在此查看{overview ? "请求与用量趋势" : `${title}状态`}。</p>
          </div>
        </div>
        <dl className="stats-strip">
          {stats.map((label) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd>
                — <span>待接入</span>
              </dd>
            </div>
          ))}
        </dl>
      </section>
      <section className="surface-card activity-panel" aria-labelledby="activity-heading">
        <div className="panel-heading">
          <div>
            <h2 id="activity-heading">{overview ? "最近请求" : `${title}记录`}</h2>
            <p className="muted">{overview ? "查看最近的请求、模型和上游活动。" : description}</p>
          </div>
          <span className="panel-icon" aria-hidden="true">
            <Layers size={20} />
          </span>
        </div>
        <div className="list-columns" aria-hidden="true">
          <span>{overview ? "请求 / 模型" : "名称 / 标识"}</span>
          <span>状态</span>
          <span>最近更新</span>
          <ArrowUpRight size={16} />
        </div>
        <div className="list-empty">
          <span className="empty-icon">
            <Database size={24} strokeWidth={1.5} aria-hidden="true" />
          </span>
          <h3>暂无可展示的记录</h3>
          <p>数据源尚未接入，当前不显示示例或模拟业务数值。</p>
        </div>
        <div className="panel-footnote">数据接入后提供加载、空数据与错误状态。</div>
      </section>
    </>
  );
}
