use super::commit::UsageTokens;
use memchr::memchr;
use serde_json::Value;

pub(super) const LINE_LIMIT: usize = 64 * 1024;
pub(super) const PRECOMMIT_LIMIT: usize = 256 * 1024;

#[derive(Default)]
pub(super) struct Scan {
    pub semantic: bool,
    pub error: bool,
    pub overflow: bool,
}

#[derive(Default)]
pub(super) struct Scanner {
    line: Vec<u8>,
    data: Vec<u8>,
    has_data: bool,
    error_event: bool,
    bom_checked: bool,
    skip_line: bool,
    skip_event: bool,
    pub usage: Option<UsageTokens>,
}

impl Scanner {
    /// Incrementally scan line/event structure without changing the raw passthrough bytes.
    pub(super) fn scan(&mut self, mut bytes: &[u8], _precommit: bool, responses: bool) -> Scan {
        let mut result = Scan::default();
        while !bytes.is_empty() {
            let newline = memchr(b'\n', bytes);
            let (fragment, remaining) = bytes.split_at(newline.unwrap_or(bytes.len()));
            if !self.skip_line {
                if fragment.len() > LINE_LIMIT - self.line.len() {
                    result.overflow = true;
                    self.skip_line = newline.is_none();
                    self.skip_event = true;
                    self.data.clear();
                    self.line.clear();
                } else {
                    self.line.extend_from_slice(fragment);
                }
            }
            let Some((_, tail)) = remaining.split_first() else {
                break;
            };
            bytes = tail;
            if self.skip_line {
                self.skip_line = false;
                continue;
            }
            self.finish_line(&mut result, responses);
            self.line.clear();
        }
        result
    }

    fn finish_line(&mut self, result: &mut Scan, responses: bool) {
        let mut line = self.line.strip_suffix(b"\r").unwrap_or(&self.line);
        if !self.bom_checked {
            self.bom_checked = true;
            line = line.strip_prefix(b"\xef\xbb\xbf").unwrap_or(line);
        }
        if line.is_empty() {
            if !self.skip_event {
                self.finish_event(result, responses);
            }
            self.data.clear();
            self.has_data = false;
            self.error_event = false;
            self.skip_event = false;
        } else if !self.skip_event {
            let (field, value) = match memchr(b':', line) {
                Some(i) => (&line[..i], &line[i + 1..]),
                None => (line, &[][..]),
            };
            let value = value.strip_prefix(b" ").unwrap_or(value);
            match field {
                b"event" => self.error_event = matches!(value, b"error" | b"response.failed"),
                b"data" => {
                    let separator = usize::from(self.has_data);
                    if self.data.len() + separator + value.len() > PRECOMMIT_LIMIT {
                        result.overflow = true;
                        self.skip_event = true;
                        self.data.clear();
                    } else {
                        if self.has_data {
                            self.data.push(b'\n');
                        }
                        self.has_data = true;
                        self.data.extend_from_slice(value);
                    }
                }
                _ => {}
            }
        }
    }

    fn finish_event(&mut self, result: &mut Scan, responses: bool) {
        let value = serde_json::from_slice::<Value>(&self.data).ok();
        let kind = value
            .as_ref()
            .and_then(|v| v.get("type"))
            .and_then(Value::as_str);
        let error = self.has_data
            && (self.error_event
                || matches!(kind, Some("error" | "response.failed"))
                || value.as_ref().is_some_and(|v| v["error"].is_object()));
        if error {
            result.error = true;
            return;
        }
        let Some(value) = value.as_ref() else {
            return;
        };
        if responses {
            if kind == Some("response.completed") {
                self.usage = UsageTokens::from_json(&value["response"]["usage"]).or(self.usage);
            }
            result.semantic |= matches!(
                kind,
                Some(
                    "response.output_text.delta"
                        | "response.reasoning_summary_text.delta"
                        | "response.function_call_arguments.delta"
                )
            ) && nonempty(&value["delta"]);
        } else {
            self.usage = UsageTokens::from_json(&value["usage"]).or(self.usage);
            result.semantic |= value["choices"].as_array().is_some_and(|choices| {
                choices.iter().any(|choice| {
                    let delta = &choice["delta"];
                    nonempty(&delta["content"])
                        || nonempty(&delta["reasoning_content"])
                        || delta["tool_calls"].as_array().is_some_and(|calls| {
                            calls
                                .iter()
                                .any(|call| nonempty(&call["function"]["arguments"]))
                        })
                })
            });
        }
    }
}

fn nonempty(value: &Value) -> bool {
    value.as_str().is_some_and(|s| !s.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_semantic_events_across_chunks_and_lines() {
        let mut scanner = Scanner::default();
        let first = scanner.scan(
            b": keepalive\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"con",
            true,
            false,
        );
        assert!(!first.semantic);
        let second = scanner.scan(b"tent\":\"hello\"}}]}\r\n\r\n", true, false);
        assert!(second.semantic);
        let mut scanner = Scanner::default();
        let error = scanner.scan(
            b"event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n",
            true,
            true,
        );
        assert!(error.error);
    }

    #[test]
    fn long_lines_and_events_are_bounded() {
        let mut scanner = Scanner::default();
        let result = scanner.scan(&vec![b'a'; LINE_LIMIT + 2], true, false);
        assert!(result.overflow);
        assert!(scanner.line.len() <= LINE_LIMIT);
        let mut scanner = Scanner::default();
        let result = scanner.scan(&[b'd'; PRECOMMIT_LIMIT + 20], true, false);
        assert!(result.overflow);
        assert!(scanner.data.len() <= PRECOMMIT_LIMIT);
    }
}
