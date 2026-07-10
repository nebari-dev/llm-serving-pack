import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { ApiKey, CreateKeyInput, CreateKeyResult } from "@/lib/types";

export const apiKeysQueryKey = ["keys"] as const;

/** The current user's API keys (GET /api/keys). */
export function useApiKeys() {
  return useQuery({
    queryKey: apiKeysQueryKey,
    queryFn: async () => {
      const data = await api.get<{ keys: ApiKey[] | null }>("/api/keys");
      return data.keys ?? [];
    },
  });
}

/** Create a new API key (POST /api/keys). */
export function useCreateKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateKeyInput) => api.post<CreateKeyResult>("/api/keys", input),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: apiKeysQueryKey });
    },
  });
}

/** Revoke an API key (DELETE /api/keys/{namespace}/{model}/{clientId}). */
export function useRevokeKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (key: Pick<ApiKey, "namespace" | "modelName" | "clientId">) =>
      api.delete(
        `/api/keys/${encodeURIComponent(key.namespace)}/${encodeURIComponent(
          key.modelName,
        )}/${encodeURIComponent(key.clientId)}`,
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: apiKeysQueryKey });
    },
  });
}
