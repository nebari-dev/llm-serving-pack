import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { Model, RawModelInfo } from "@/lib/types";

export const modelsQueryKey = ["models"] as const;

/**
 * Models the user may create keys for (GET /api/models), normalized from the
 * PascalCase Go struct shape into the camelCase {@link Model} the UI uses.
 */
export function useModels() {
  return useQuery({
    queryKey: modelsQueryKey,
    queryFn: async (): Promise<Model[]> => {
      const data = await api.get<{ models: RawModelInfo[] | null }>("/api/models");
      return (data.models ?? []).map((m) => ({ name: m.Name, namespace: m.Namespace }));
    },
  });
}
