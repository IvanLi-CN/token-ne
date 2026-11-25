import type { AdminUser, AuthConfig, LoginRequest } from "../../lib/api";
import type { DemoState } from "./state";

/**
 * The production session boundary, satisfied instead of bypassed (REQ-DEMO-003).
 * Only the `session-expired` scene starts closed; a successful local login in
 * this page lifetime opens it, and a reload re-arms the 401.
 */
export function me(state: DemoState): AdminUser | undefined {
  return state.authenticated ? state.admin : undefined;
}

export function login(state: DemoState, body: LoginRequest): AdminUser | undefined {
  // Any demo credential works, but the password never leaves this function.
  if (!body.username.trim() || !body.password) return undefined;
  state.authenticated = true;
  return state.admin;
}

export function logout(state: DemoState): void {
  state.authenticated = false;
}

/**
 * OIDC stays unavailable on purpose: its start endpoint is a document
 * navigation the fetch mock cannot intercept, so the login page must not
 * render that path.
 */
export function config(): AuthConfig {
  return {
    local_login_enabled: true,
    oidc_enabled: false,
    oidc_login_available: false,
  };
}
