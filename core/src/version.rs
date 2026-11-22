pub const VERSION: &str = env!("TOKEN_NE_VERSION");

#[cfg(test)]
mod tests {
    #[test]
    fn version_comes_from_app_version() {
        let expected = option_env!("APP_VERSION").unwrap_or(env!("CARGO_PKG_VERSION"));
        assert_eq!(super::VERSION, expected);
    }

    #[test]
    fn meta_reports_the_build_version() {
        assert_eq!(crate::telemetry::meta()["version"], super::VERSION);
    }
}
