import type { Billing, PriceTier } from "../lib/api";

/** Money keeps at least two and at most four decimals so columns align. */
export function formatMoney(value: number | null | undefined): string | null {
  if (value == null) return null;
  if (value === 0) return "免费";
  const fixed = value.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
  const [whole, fraction = ""] = fixed.split(".");
  return `$${whole}.${`${fraction}00`.slice(0, Math.max(2, fraction.length))}`;
}

export function MissingPrice({ label }: { label: string }) {
  return (
    <span
      className="price-missing"
      role="img"
      aria-label={`${label}未提供`}
      title={`${label}未提供`}
    >
      —
    </span>
  );
}

export function Money({ value, label }: { value: number | null | undefined; label: string }) {
  const text = formatMoney(value);
  return text === null ? <MissingPrice label={label} /> : <span className="price-num">{text}</span>;
}

export function AudioMoney({ billing }: { billing: Billing }) {
  const input = formatMoney(billing.audio_input_usd_per_mtok);
  const output = formatMoney(billing.audio_output_usd_per_mtok);
  if (input === null && output === null) return <MissingPrice label="音频价格" />;
  return (
    <span className="price-num price-secondary">
      {input ?? "—"} <span className="price-sep">/</span> {output ?? "—"}
    </span>
  );
}

export function TierPrices({ tiers }: { tiers: PriceTier[] | undefined }) {
  if (!tiers || tiers.length === 0) return <MissingPrice label="阶梯价格" />;
  return (
    <div className="tier-list">
      {tiers.map((tier) => (
        <div className="tier-line" key={tier.threshold_tokens}>
          <span className="tier-threshold">≥{tier.threshold_tokens.toLocaleString()}</span>
          <span className="price-num price-secondary">
            {formatMoney(tier.input_usd_per_mtok) ?? "—"} <span className="price-sep">/</span>{" "}
            {formatMoney(tier.output_usd_per_mtok) ?? "—"}
          </span>
        </div>
      ))}
    </div>
  );
}
