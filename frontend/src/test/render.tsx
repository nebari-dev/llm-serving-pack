import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderOptions, render } from "@testing-library/react";
import { createStore, Provider as JotaiProvider } from "jotai";
import type { ReactElement, ReactNode } from "react";

import { THEME_STORAGE_KEY } from "@/app/theme";
import { ThemeProvider } from "@/hooks/theme-provider";

type Store = ReturnType<typeof createStore>;

/** App providers for tests; clients/stores are passed in so each render stays isolated. */
export function TestProviders({
  queryClient,
  store,
  children,
}: {
  queryClient: QueryClient;
  store: Store;
  children: ReactNode;
}) {
  return (
    <QueryClientProvider client={queryClient}>
      <JotaiProvider store={store}>
        <ThemeProvider storageKey={THEME_STORAGE_KEY}>{children}</ThemeProvider>
      </JotaiProvider>
    </QueryClientProvider>
  );
}

/**
 * Render a component wrapped in the app's providers (fresh QueryClient + Jotai
 * store per call, plus ThemeProvider) so tests are isolated from one another.
 */
export function renderWithProviders(ui: ReactElement, options?: Omit<RenderOptions, "wrapper">) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const store = createStore();

  return render(ui, {
    wrapper: ({ children }) => (
      <TestProviders queryClient={queryClient} store={store}>
        {children}
      </TestProviders>
    ),
    ...options,
  });
}
