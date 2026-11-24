export function formatNumber(value: number | null | undefined) {
  return value == null
    ? "未配置"
    : new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }).format(value);
}
export function formatMoney(value: number | null | undefined) {
  return value == null
    ? "未配置"
    : new Intl.NumberFormat("zh-CN", {
        style: "currency",
        currency: "USD",
        maximumFractionDigits: 6,
      }).format(value);
}
/** 价格单位是 USD / 百万 tokens；null 未知，0 免费。 */
export function formatPrice(value: number | null | undefined) {
  return value == null ? "未配置" : value === 0 ? "免费" : `$${value}`;
}
export function formatTime(value: string | null | undefined) {
  return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "未记录";
}
