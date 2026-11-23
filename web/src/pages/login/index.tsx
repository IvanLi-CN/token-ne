import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { Button, Input, Spinner } from "../../components/primitives";
import { authApi, type LoginRequest } from "../../lib/api";
import { safeReturnPath } from "../../lib/guards";
import { queryKeys } from "../../lib/query-client";

export default function LoginPage() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const client = useQueryClient();
  const config = useQuery({
    queryKey: queryKeys.authConfig,
    queryFn: ({ signal }) => authApi.config(signal),
    retry: false,
  });
  const returnTo = safeReturnPath(params.get("returnTo"));
  const localEnabled = config.data?.local_login_enabled ?? false;
  const oidcAvailable = config.data?.oidc_login_available ?? false;
  const oidcError = params.get("error");
  const login = useMutation({
    mutationFn: (values: LoginRequest) => authApi.login(values),
    onSuccess: (user) => {
      client.clear();
      client.setQueryData(queryKeys.session, user);
      void navigate(returnTo, { replace: true });
    },
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!login.isPending && username.trim() && password)
      login.mutate({ username: username.trim(), password });
  }
  return (
    <main id="main-content" className="login-layout">
      <div className="login-container">
        <p className="wordmark">
          <span className="brand-mark" aria-hidden="true">
            t/n
          </span>{" "}
          token-ne
        </p>
        <section className="surface-card login-panel" aria-labelledby="login-title">
          <h1 id="login-title">管理员登录</h1>
          {config.isPending && <Spinner label="正在加载登录配置" />}
          {config.isError && (
            <div role="alert" className="field-error">
              <p>无法加载登录配置，请重试。</p>
              <Button
                type="button"
                variant="secondary"
                onClick={() => void config.refetch()}
                disabled={config.isFetching}
              >
                {config.isFetching ? "重试中…" : "重试"}
              </Button>
            </div>
          )}
          {config.isSuccess && (
            <p className="muted">
              {localEnabled ? "使用管理员凭据登录。" : "当前仅允许通过 OIDC 登录。"}
            </p>
          )}
          {oidcError && (
            <p role="alert" className="field-error">
              登录失败，请重试。
            </p>
          )}
          {config.isSuccess && !localEnabled && !oidcAvailable && (
            <p role="alert" className="field-error">
              当前没有可用的登录方式，请使用 break-glass 恢复本地登录。
            </p>
          )}
          {config.isSuccess && localEnabled && (
            <form onSubmit={submit} aria-busy={login.isPending}>
              <div className="field">
                <label htmlFor="username">用户名</label>
                <Input
                  id="username"
                  name="username"
                  autoComplete="username"
                  required
                  value={username}
                  onChange={(event) => setUsername(event.target.value)}
                  aria-invalid={login.isError}
                  aria-describedby={login.isError ? "login-error" : undefined}
                />
              </div>
              <div className="field">
                <label htmlFor="password">密码</label>
                <Input
                  id="password"
                  name="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  aria-invalid={login.isError}
                  aria-describedby={login.isError ? "login-error" : undefined}
                />
              </div>
              {login.isError && (
                <p id="login-error" role="alert" className="field-error">
                  用户名或密码错误
                </p>
              )}
              <Button type="submit" disabled={login.isPending || !username.trim() || !password}>
                {login.isPending ? <Spinner label="登录中" /> : "登录"}
              </Button>
            </form>
          )}
          {config.isSuccess && oidcAvailable && (
            <a
              className="button button-primary mt-5 flex min-h-11 w-full items-center justify-center rounded-xl"
              href={`/api/auth/oidc/start?returnTo=${encodeURIComponent(returnTo)}`}
            >
              使用 OIDC 登录
            </a>
          )}
          <p className="login-footnote">登录后使用安全会话</p>
        </section>
      </div>
    </main>
  );
}
