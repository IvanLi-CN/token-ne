import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SecondPhaseSurface } from "../../app/phase";
import { PageHeader, PageTitle } from "../../components/primitives";
import { pricesApi } from "../../lib/api";
import { queryKeys } from "../../lib/query-client";
import { modelsApi } from "../models/api";
import { errorMessage } from "../models/management";
import { PriceSourceCatalogSection } from "../price-sync/catalog";
import { upstreamsApi } from "../upstreams/api";
import { PriceProfilesSection } from "./profiles";
import { PriceQuotesSection } from "./quotes";

function PricesContent() {
  const client = useQueryClient();
  const models = useQuery({
    queryKey: queryKeys.models,
    queryFn: ({ signal }) => modelsApi.list(signal),
  });
  const upstreams = useQuery({
    queryKey: queryKeys.upstreams,
    queryFn: ({ signal }) => upstreamsApi.list(signal),
  });
  const profiles = useQuery({
    queryKey: queryKeys.prices,
    queryFn: ({ signal }) => pricesApi.list(signal),
  });
  const refresh = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: queryKeys.prices }),
      client.invalidateQueries({ queryKey: queryKeys.models }),
    ]);
  };
  const error = models.isError
    ? errorMessage(models.error)
    : profiles.isError
      ? errorMessage(profiles.error)
      : undefined;
  return (
    <>
      <PageHeader
        title="价格"
        description="先浏览受控价格源并按需加入全局模型，再获取报价、审核差异并应用；上游覆盖与锁定状态同页维护。"
      />
      <PriceSourceCatalogSection />
      <PriceProfilesSection
        models={models.data}
        profiles={profiles.data}
        upstreams={upstreams.data}
        loading={models.isPending || profiles.isPending}
        error={error}
        onRetry={() => {
          void models.refetch();
          void profiles.refetch();
        }}
        onChanged={refresh}
      />
      <PriceQuotesSection models={models.data} upstreams={upstreams.data} onApplied={refresh} />
    </>
  );
}

export default function PricesPage() {
  return (
    <>
      <PageTitle title="价格" />
      <SecondPhaseSurface>
        <PricesContent />
      </SecondPhaseSurface>
    </>
  );
}
