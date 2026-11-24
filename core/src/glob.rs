//! 受限 Glob：`*` 与 `?`，区分大小写，匹配完整字符串。
//!
//! 语言定义以 `docs/protocol.md` 为准：只有 `*`（任意长度，含空串）和 `?`
//! （恰好一个 Unicode 标量）是通配符；方括号、花括号、反斜线和 ASCII 控制字符
//! 不是合法字符，正则或相似度匹配不属于该语言。路由规则与上游承接策略共用本模块，
//! 不得改用 regex crate。
//!
//! Go 侧必须使用同一语言定义：按 rune 而非 byte 判定 `?`，`*` 可匹配空串，
//! 匹配范围是完整字符串。`matches` 是标准双指针回溯算法，`overlap` 用同一套
//! token 语义构造乘积自动机，因此两处不会出现“能否匹配”与“是否重叠”分歧。

/// 模式按 Unicode 标量计数，与配置 schema 的 `maxLength` 对齐。
pub const MAX_CHARS: usize = 256;

/// 校验受限 Glob 模式：非空、最多 256 个标量，且只含合法字符。
pub fn validate(pattern: &str) -> bool {
    !pattern.is_empty()
        && pattern.chars().count() <= MAX_CHARS
        && !pattern
            .chars()
            .any(|c| matches!(c, '[' | ']' | '{' | '}' | '\\') || c.is_ascii_control())
}

/// 完整字符串匹配：`*` 匹配任意长度（含空串），`?` 匹配恰好一个 Unicode 标量。
pub fn matches(pattern: &str, text: &str) -> bool {
    let pattern: Vec<char> = pattern.chars().collect();
    let text: Vec<char> = text.chars().collect();
    let (mut p, mut t) = (0, 0);
    let mut star: Option<usize> = None;
    let mut resume = 0;
    while t < text.len() {
        if p < pattern.len() && (pattern[p] == '?' || pattern[p] == text[t]) {
            p += 1;
            t += 1;
        } else if p < pattern.len() && pattern[p] == '*' {
            star = Some(p);
            resume = t;
            p += 1;
        } else if let Some(s) = star {
            // 让上一个 `*` 多吃一个字符后重试。
            resume += 1;
            p = s + 1;
            t = resume;
        } else {
            return false;
        }
    }
    pattern[p..].iter().all(|c| *c == '*')
}

/// 两个模式是否存在共同可匹配的字符串。
///
/// 仅对通过 [`validate`] 的模式定义；调用方必须先完成语法校验。
pub fn overlap(a: &str, b: &str) -> bool {
    debug_assert!(validate(a) && validate(b));
    let p: Vec<char> = a.chars().collect();
    let q: Vec<char> = b.chars().collect();
    let literals: std::collections::HashSet<char> = p
        .iter()
        .chain(&q)
        .copied()
        .filter(|c| !matches!(c, '*' | '?'))
        .collect();
    // 合法模式拒绝 ASCII 控制字符，因此 U+0000 可代表“所有非字面量字符”。
    let sentinel = '\u{0}';
    debug_assert!(!literals.contains(&sentinel));
    let mut alphabet: Vec<char> = literals.into_iter().collect();
    alphabet.push(sentinel);
    let width = q.len() + 1;
    let mut seen = vec![false; (p.len() + 1) * width];
    let mut stack = vec![(0usize, 0usize)];
    seen[0] = true;
    while let Some((i, j)) = stack.pop() {
        if i == p.len() && j == q.len() {
            return true;
        }
        // `*` 可以结束当前匹配，等价于跳过该 token。
        if p.get(i) == Some(&'*') {
            visit(&mut stack, &mut seen, width, (i + 1, j));
        }
        if q.get(j) == Some(&'*') {
            visit(&mut stack, &mut seen, width, (i, j + 1));
        }
        for &c in &alphabet {
            if let (Some(ni), Some(nj)) = (consume(&p, i, c), consume(&q, j, c)) {
                visit(&mut stack, &mut seen, width, (ni, nj));
            }
        }
    }
    false
}

fn visit(stack: &mut Vec<(usize, usize)>, seen: &mut [bool], width: usize, (i, j): (usize, usize)) {
    let slot = &mut seen[i * width + j];
    if !*slot {
        *slot = true;
        stack.push((i, j));
    }
}

/// 在 `index` 处消费字符 `c`：`*` 原地重复，`?`/字面量前进一步。
fn consume(pattern: &[char], index: usize, c: char) -> Option<usize> {
    match *pattern.get(index)? {
        '*' => Some(index),
        '?' => Some(index + 1),
        literal if literal == c => Some(index + 1),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{MAX_CHARS, matches, overlap, validate};

    #[test]
    fn wildcard_semantics_are_full_string_and_case_sensitive() {
        assert!(matches("gpt-*", "gpt-5"));
        assert!(matches("gpt-*", "gpt-"));
        assert!(matches("*", ""));
        assert!(matches("gpt-?", "gpt-5"));
        assert!(!matches("gpt-?", "gpt-55"));
        assert!(!matches("gpt-?", "gpt-"));
        assert!(matches("gpt-??", "gpt-猫猫"));
        assert!(!matches("GPT-*", "gpt-5"));
        assert!(matches("a*b*c", "aXXbYYc"));
        assert!(!matches("a*b*c", "aXXbYY"));
        assert!(matches("*gpt*", "my-gpt-5"));
        assert!(!matches("gpt-5", "gpt-5.1"));
    }

    #[test]
    fn validation_matches_the_frozen_schema_language() {
        for ok in ["gpt-*", "*", "?a?", "chat-??", "模型-*", "gpt-4o*"] {
            assert!(validate(ok), "rejected {ok:?}");
        }
        let too_long = "x".repeat(MAX_CHARS + 1);
        for bad in [
            "",
            "gpt-[45]",
            "gpt-{45}",
            "a\\b",
            "a\tb",
            "a\u{7f}b",
            too_long.as_str(),
        ] {
            assert!(!validate(bad), "accepted {bad:?}");
        }
    }

    #[test]
    fn overlap_uses_the_same_language() {
        assert!(overlap("*", "anything"));
        assert!(overlap("gpt-*", "gpt-4o"));
        assert!(overlap("gpt-4?", "gpt-45"));
        assert!(overlap("chat-??", "chat-*"));
        assert!(overlap("a*", "*a"));
        assert!(overlap("gpt-*", "gpt-4o*"));
        assert!(!overlap("gpt-4*", "gpt-5*"));
        assert!(!overlap("a*c", "a*d"));
        assert!(!overlap("ab?", "ab"));
        assert!(!overlap("chat-?", "chat-??"));
        assert!(overlap("chat-?*", "chat-??"));
    }
}
