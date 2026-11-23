import {
  createBrowserRouter,
  Link,
  type LoaderFunctionArgs,
  type RouteObject,
  redirect,
  useRevalidator,
  useRouteError,
} from "react-router";
import { ErrorState, PageHeader, Spinner } from "../components/primitives";
import { ApiError, authApi, onUnauthorized } from "../lib/api";
import { loginPath } from "../lib/guards";
import { queryClient } from "../lib/query-client";
import CoresPage from "../pages/cores";
import KeysPage from "../pages/keys";
import LoginPage from "../pages/login";
import ModelsPage from "../pages/models";
import OverviewPage from "../pages/overview";
import RequestsPage from "../pages/requests";
import SettingsPage from "../pages/settings";
import UpstreamsPage from "../pages/upstreams";
import { AppShell } from "./shell";

export async function sessionLoader({ request }: LoaderFunctionArgs) {
  try {
    return await authApi.me(request.signal);
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      queryClient.clear();
      const url = new URL(request.url);
      throw redirect(loginPath(`${url.pathname}${url.search}`));
    }
    throw error;
  }
}
function RouteError() {
  const error = useRouteError();
  const revalidator = useRevalidator();
  return (
    <main className="main-content">
      <PageHeader title="暂时无法打开页面" />
      <ErrorState
        message={error instanceof ApiError ? error.message : "页面加载失败，请重试。"}
        onRetry={() => void revalidator.revalidate()}
      />
      <Link to="/login">返回登录</Link>
    </main>
  );
}
function NotFound() {
  return (
    <>
      <PageHeader title="页面未找到" description="请从主导航选择页面。" />
      <Link to="/">返回总览</Link>
    </>
  );
}
export const routes: RouteObject[] = [
  { path: "/login", Component: LoginPage, ErrorBoundary: RouteError },
  {
    id: "app",
    path: "/",
    Component: AppShell,
    loader: sessionLoader,
    ErrorBoundary: RouteError,
    HydrateFallback: () => (
      <main className="main-content">
        <h1>Phase 1 control plane</h1>
        <Spinner label="正在验证会话" />
      </main>
    ),
    children: [
      { index: true, Component: OverviewPage },
      { path: "requests", Component: RequestsPage },
      { path: "models", Component: ModelsPage },
      { path: "upstreams", Component: UpstreamsPage },
      { path: "keys", Component: KeysPage },
      { path: "cores", Component: CoresPage },
      { path: "settings", Component: SettingsPage },
      { path: "*", Component: NotFound },
    ],
  },
];
export function createAppRouter() {
  const router = createBrowserRouter(routes);
  onUnauthorized(() => {
    queryClient.clear();
    const { pathname, search, hash } = router.state.location;
    if (pathname !== "/login")
      void router.navigate(loginPath(`${pathname}${search}${hash}`), { replace: true });
  });
  return router;
}
