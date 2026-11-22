fn main() {
    let version = std::env::var("APP_VERSION")
        .ok()
        .filter(|value| !value.trim().is_empty())
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string());
    println!("cargo:rustc-env=TOKEN_NE_VERSION={version}");
    println!("cargo:rerun-if-env-changed=APP_VERSION");
}
