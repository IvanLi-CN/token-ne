//! 受限 Glob：`*` 与 `?`，区分大小写，匹配完整字符串。
//!
//! 语言定义以 `docs/protocol.md` 为准：只有 `*`（任意长度，含空串）和 `?`
//! （恰好一个 Unicode 标量）是通配符；方括号、花括号、反斜线和 ASCII 控制字符
//! 不是合法字符，正则或相似度匹配不属于该语言。路由规则与上游承接策略共用本模块，
//! 不得改用 regex crate。
//!
//! Go 侧必须使用同一语言定义：按 rune 而非 byte 判定 `?`，`*` 可匹配空串，
//! 匹配范围是完整字符串。`matches` 是标准双指针回溯算法，与 `modelglob.Match`
//! 逐 rune 对齐，因此两处不会出现“能否匹配”分歧。

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
        // 先判通配符再做字面比较：名字本身可能含 `*`，先按字面消费会让 `*`
        // 永久吞掉回溯点，与 Go 侧 modelglob 的行为不一致。
        if p < pattern.len() && pattern[p] == '*' {
            star = Some(p);
            resume = t;
            p += 1;
        } else if p < pattern.len() && (pattern[p] == '?' || pattern[p] == text[t]) {
            p += 1;
            t += 1;
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

#[cfg(test)]
mod tests {
    use super::{MAX_CHARS, matches, validate};

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
        // 名字本身可以含 `*`：字面比较不能先把通配符当普通字符吃掉。
        assert!(matches("a*", "a*b"));
        assert!(matches("*", "**"));
        assert!(matches("*o", "*-o"));
        assert!(matches("a*b", "a**b"));
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
}
