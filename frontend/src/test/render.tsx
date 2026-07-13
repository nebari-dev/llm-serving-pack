import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderOptions, render } from "@testing-library/react";
import { Provider as JotaiProvider } from "jotai";
import type { ReactElement, ReactNode } from "react";

import { ThemeProvider } from "@/providers/ThemeProvider";

/**
 * Render a component wrapped in the app's providers (fresh QueryClient + Jotai
 * store per call, plus ThemeProvider) so tests are isolated from one another.
 */
export function renderWithProviders(ui: ReactElement, options?: Omit<RenderOptions, "wrapper">) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <JotaiProvider>
          <ThemeProvider>{children}</ThemeProvider>
        </JotaiProvider>
      </QueryClientProvider>
    );
  }

  return render(ui, { wrapper: Wrapper, ...options });
}
