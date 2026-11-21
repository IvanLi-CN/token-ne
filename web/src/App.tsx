import { type DataRouter, RouterProvider } from "react-router";
import { AppProviders } from "./app/providers";
import { createAppRouter } from "./app/router";

let browserRouter: DataRouter | undefined;
export default function App({ router }: { router?: DataRouter }) {
  if (!router && !browserRouter) browserRouter = createAppRouter();
  const activeRouter = router ?? browserRouter;
  if (!activeRouter) throw new Error("Application router is missing");
  return (
    <AppProviders>
      <RouterProvider router={activeRouter} />
    </AppProviders>
  );
}
