import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Auth/data here is short-lived and cheap to refetch; avoid masking real
      // failures (e.g. an expired session) behind silent retries.
      retry: false,
      staleTime: 30_000,
      refetchOnWindowFocus: false,
    },
  },
});
