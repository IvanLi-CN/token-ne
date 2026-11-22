import { useQuery } from "@tanstack/react-query";
import { versionApi } from "../lib/api";
import { queryKeys } from "../lib/query-client";

export function VersionBadge() {
  const { data, isError } = useQuery({
    queryKey: queryKeys.version,
    queryFn: ({ signal }) => versionApi.get(signal),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  return (
    <span className="version-badge">
      {isError
        ? "版本不可用"
        : data
          ? `版本 ${data.version} · ${data.commit.slice(0, 7)}`
          : "版本…"}
    </span>
  );
}
