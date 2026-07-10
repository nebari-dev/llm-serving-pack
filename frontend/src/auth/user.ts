import { useMemo } from "react";

import { getKeycloakInstance } from "./keycloak";

export type User = {
  name: string;
  email: string;
};

/** The authenticated user, read from the Keycloak ID token. */
export function useUser(): { user: User | null } {
  const user = useMemo<User | null>(() => {
    const kc = getKeycloakInstance();
    if (!kc?.authenticated || !kc.idTokenParsed) {
      return null;
    }

    const parsed = kc.idTokenParsed as Record<string, string>;
    const name = parsed.name || parsed.preferred_username || parsed.sub || "User";
    const email = parsed.email || "";

    return { name, email };
  }, []);

  return { user };
}
