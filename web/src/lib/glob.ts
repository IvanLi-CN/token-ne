/**
 * 受限 Glob：`*` 匹配任意长度（含空串），`?` 匹配恰好一个 Unicode 标量，
 * 整串匹配且区分大小写。与 `modelglob` / `core::glob` 的语言定义一致。
 *
 * 按码点而不是 UTF-16 码元遍历，因此一个 emoji 只消耗一个 `?`。
 */
export function globMatches(pattern: string, value: string): boolean {
  const patternChars = [...pattern];
  const valueChars = [...value];
  let patternIndex = 0;
  let valueIndex = 0;
  let star = -1;
  let starMatch = 0;
  while (valueIndex < valueChars.length) {
    if (patternIndex < patternChars.length && patternChars[patternIndex] === "*") {
      star = patternIndex;
      patternIndex += 1;
      starMatch = valueIndex;
    } else if (
      patternIndex < patternChars.length &&
      (patternChars[patternIndex] === "?" || patternChars[patternIndex] === valueChars[valueIndex])
    ) {
      patternIndex += 1;
      valueIndex += 1;
    } else if (star >= 0) {
      patternIndex = star + 1;
      starMatch += 1;
      valueIndex = starMatch;
    } else return false;
  }
  while (patternIndex < patternChars.length && patternChars[patternIndex] === "*")
    patternIndex += 1;
  return patternIndex === patternChars.length;
}
