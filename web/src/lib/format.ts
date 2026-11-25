export function formatNumber(value: number | null | undefined) {
  return value == null
    ? "未配置"
    : new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }).format(value);
}
/** Money keeps at least two and at most four decimals so columns align. */
export function formatMoney(value: number | null | undefined): string | null {
  if (value == null) return null;
  if (value === 0) return "免费";
  const fixed = value.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
  const [whole, fraction = ""] = fixed.split(".");
  if (Math.abs(value) >= 0.0001) {
    return `$${whole}.${`${fraction}00`.slice(0, Math.max(2, fraction.length))}`;
  }
  // Sub-cent prices must not collapse into $0.00; widen until a digit survives.
  let decimals = 6;
  while (decimals < 12 && Number(Math.abs(value).toFixed(decimals)) === 0) decimals += 1;
  const tiny = value.toFixed(decimals).replace(/0+$/, "").replace(/\.$/, "");
  return `$${tiny}`;
}
/** 价格单位是 USD / 百万 tokens；null 未知，0 免费，与金额列共用同一格式。 */
export function formatPrice(value: number | null | undefined) {
  return formatMoney(value) ?? "未配置";
}
export function formatTime(value: string | null | undefined) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "未记录";
}
