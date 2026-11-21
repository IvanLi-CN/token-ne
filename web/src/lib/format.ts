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
