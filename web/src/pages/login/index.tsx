import { useMutation, useQueryClient } from "@tanstack/react-query";
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
  const login = useMutation({
    mutationFn: (values: LoginRequest) => authApi.login(values),
    onSuccess: (user) => {
      client.clear();
      client.setQueryData(queryKeys.session, user);
      void navigate(safeReturnPath(params.get("returnTo")), { replace: true });
    },
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!login.isPending && username.trim() && password)
      login.mutate({ username: username.trim(), password });
  }
  return (
    <main id="main-content" className="login-layout">
      <div className="login-instrument">
        <section className="login-readout" aria-label="系统信息">
          <p className="wordmark">
            <span className="brand-mark" aria-hidden="true">
              t/n
            </span>{" "}
            token-ne
          </p>
          <p className="login-section-label">系统状态</p>
          <dl className="system-readout">
            <div>
              <dt>运行模式</dt>
              <dd>管理员</dd>
            </div>
            <div>
              <dt>会话状态</dt>
              <dd>未登录</dd>
            </div>
            <div>
              <dt>访问方式</dt>
              <dd>同源会话</dd>
            </div>
          </dl>
        </section>
        <section className="login-panel" aria-labelledby="login-title">
          <h1 id="login-title">管理员登录</h1>
          <p className="muted">使用管理员凭据登录。</p>
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
          <p className="login-footnote">登录后使用安全会话</p>
        </section>
      </div>
    </main>
  );
}
