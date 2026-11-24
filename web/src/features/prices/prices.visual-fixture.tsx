// Test-only, login-free Prices visual fixture. Never imported by the production entry.
const params = new URLSearchParams(location.search);
params.set("page", "prices");
history.replaceState(null, "", `${location.pathname}?${params}${location.hash}`);
void import("../models/management.visual-fixture");
