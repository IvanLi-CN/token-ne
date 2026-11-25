import { ApiError, type Billing, type QuoteMatchMethod, type QuoteStatus } from "../../lib/api";
import { formatPrice, formatTime } from "../../lib/format";

export const quoteStatusMeta: Record<
  QuoteStatus,
  {
    label: string;
    tone: "success" | "warning" | "error" | "neutral";
    selectable: boolean;
    review: boolean;
    hint: string;
  }
> = {
  safe: {
    label: "安全",
    tone: "success",
    selectable: true,
    review: false,
    hint: "精确匹配且无冲突，可批量应用。",
  },
  needs_review: {
    label: "需审核",
    tone: "warning",
    selectable: true,
    review: true,
    hint: "标准化建议；必须逐项确认后才能应用。",
  },
  conflict: {
    label: "冲突",
    tone: "error",
    selectable: true,
    review: true,
    hint: "来源之间不一致，或目标已锁定；必须逐项确认后才能应用。",
  },
  missing: {
    label: "缺失",
    tone: "neutral",
    selectable: false,
    review: false,
    hint: "来源中没有该模型；保留当前价格。",
  },
  ignored: {
    label: "已忽略",
    tone: "neutral",
    selectable: false,
    review: false,
    hint: "来源价格与当前价格一致，无需变更。",
  },
};

export const matchMethodLabels: Record<QuoteMatchMethod, string> = {
  explicit: "显式映射",
  upstream_exact: "上游 ID 精确",
  model_exact: "模型 ID 精确",
  normalized: "标准化建议",
  none: "未匹配",
};

export type PriceRejectionAction = "resync" | "review" | "locked" | "select" | "refresh";

export interface PriceRejection {
  code: string;
  title: string;
  nextAction: string;
  action: PriceRejectionAction;
}

/** 每种拒绝码都必须给出不同的下一步动作；未知错误回退到通用错误展示。 */
export function priceRejection(error: unknown): PriceRejection | undefined {
  if (!(error instanceof ApiError)) return undefined;
  switch (error.code) {
    case "stale_price_quote":
      return {
        code: error.code,
        title: "报价基线已过期（stale_price_quote）",
        nextAction: "目标价格在报价生成后已改变，本批未修改任何价格。重新获取报价并审核新的差异。",
        action: "resync",
      };
    case "quote_review_required":
      return {
        code: error.code,
        title: "非安全报价未逐项审核（quote_review_required）",
        nextAction: "在报价行勾选“已审核”后重新应用；本批未修改任何价格。",
        action: "review",
      };
    case "price_profile_locked":
      return {
        code: error.code,
        title: "目标价格档案已锁定（price_profile_locked）",
        nextAction: "在确认框中勾选“覆盖已锁定档案”后重新应用；本批未修改任何价格。",
        action: "locked",
      };
    case "quote_not_applicable":
      return {
        code: error.code,
        title: "报价不适用（quote_not_applicable）",
        nextAction: "取消选择缺少目标或无法解析作用域的报价，或重新获取报价。",
        action: "select",
      };
    case "price_run_not_ready":
      return {
        code: error.code,
        title: "批次尚未就绪（price_run_not_ready）",
        nextAction: "等待获取完成，或刷新批次状态后重试。",
        action: "refresh",
      };
    default:
      return undefined;
  }
}

export function BillingCell({ billing }: { billing: Billing | null | undefined }) {
  if (!billing) return <span className="muted">无价格</span>;
  const rows: Array<[string, number | null | undefined]> = [
    ["输入", billing.input_usd_per_mtok],
    ["缓存", billing.cached_input_usd_per_mtok],
    ["缓存写", billing.cache_write_usd_per_mtok],
    ["音频入", billing.audio_input_usd_per_mtok],
    ["音频出", billing.audio_output_usd_per_mtok],
    ["推理", billing.reasoning_usd_per_mtok],
    ["输出", billing.output_usd_per_mtok],
  ];
  return (
    <div className="grid gap-0.5 text-xs">
      {rows.map(([label, price]) => (
        <span key={label}>
          {label} {formatPrice(price)}
        </span>
      ))}
      {(billing.tiers ?? []).map((tier) => (
        <span key={tier.threshold_tokens} className="muted">
          ≥{tier.threshold_tokens.toLocaleString()} tokens：入{" "}
          {formatPrice(tier.input_usd_per_mtok)} / 出 {formatPrice(tier.output_usd_per_mtok)}
        </span>
      ))}
    </div>
  );
}

export function RevisionCell({
  revisionId,
  createdAt,
}: {
  revisionId: string | null;
  createdAt: string | null;
}) {
  if (!revisionId)
    return (
      <div>
        <span className="muted">生成时不存在</span>
        <div className="muted text-xs">应用前目标必须仍不存在</div>
      </div>
    );
  return (
    <div>
      <code className="text-xs break-all">{revisionId.slice(0, 8)}</code>
      <div className="muted text-xs">{formatTime(createdAt)}</div>
    </div>
  );
}
