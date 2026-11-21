import { type QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { queryClient } from "../lib/query-client";
import { AdaptiveStateProvider } from "./adaptive-state";
import { ThemeProvider } from "./theme";

export function AppProviders({
  children,
  client = queryClient,
}: {
  children: ReactNode;
  client?: QueryClient;
}) {
  return (
    <QueryClientProvider client={client}>
      <AdaptiveStateProvider>
        <ThemeProvider>{children}</ThemeProvider>
      </AdaptiveStateProvider>
    </QueryClientProvider>
  );
}
