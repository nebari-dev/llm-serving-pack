import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { CurrentUser } from "@/lib/types";

export const currentUserQueryKey = ["me"] as const;

/** The authenticated user (GET /api/me). */
export function useCurrentUser() {
  return useQuery({
    queryKey: currentUserQueryKey,
    queryFn: () => api.get<CurrentUser>("/api/me"),
  });
}
