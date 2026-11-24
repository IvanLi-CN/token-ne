import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { EmptyState, ErrorState, Spinner } from "../components/primitives";
import { versionApi } from "../lib/api";
import { queryKeys } from "../lib/query-client";

/**
 * 部署阶段只由 GET /api/version 决定。缺省、失败或字段缺失都按第一期处理，
 * 避免在未部署二期服务端时暴露未实现的能力。
 */
export function usePhaseFeatures() {
  const version = useQuery({
    queryKey: queryKeys.version,
    queryFn: ({ signal }) => versionApi.get(signal),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  return {
    version,
    pending: version.isPending,
    failed: version.isError,
    secondPhase: version.data?.features?.second_phase_model_management === true,
  };
}

/** 独立的 Prices 路由在二期关闭时给出明确说明，不渲染任何二期控件。 */
export function SecondPhaseSurface({ children }: { children: ReactNode }) {
  const { secondPhase, pending, failed, version } = usePhaseFeatures();
  if (pending) return <Spinner label="正在读取部署阶段" />;
  if (failed)
    return (
      <ErrorState
        title="无法读取部署阶段"
        message="版本接口不可用；为避免展示未部署的能力，二期管理保持关闭。"
        onRetry={() => void version.refetch()}
      />
    );
  if (!secondPhase)
    return (
      <EmptyState title="第二期管理未启用">
        当前部署返回
        features.second_phase_model_management=false；价格、路由规则、目录同步与承接策略保持关闭。
      </EmptyState>
    );
  return <>{children}</>;
}
