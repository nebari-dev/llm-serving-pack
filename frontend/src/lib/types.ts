// Shapes returned by the key-manager API (key-manager/internal/api/handler.go).

/** GET /api/me */
export interface CurrentUser {
  username: string;
  name: string;
  email: string;
  groups: string[];
}

/**
 * Raw entry from GET /api/models. The Go `ModelInfo` struct carries no JSON
 * tags, so fields serialize with their PascalCase Go names. `useModels`
 * normalizes this into {@link Model}; prefer that everywhere else.
 */
export interface RawModelInfo {
  Name: string;
  Namespace: string;
  ModelName: string;
  Public: boolean;
  Groups: string[] | null;
  Passthrough: boolean;
  Provider: string;
}

/** Normalized model the UI works with. */
export interface Model {
  /** LLMModel name — the value POSTed as `modelName` when creating a key. */
  name: string;
  namespace: string;
}

/** GET /api/keys */
export interface ApiKey {
  clientId: string;
  creator: string;
  description: string;
  createdAt: string;
  modelName: string;
  namespace: string;
}

/** POST /api/keys request body */
export interface CreateKeyInput {
  modelName: string;
  description: string;
}

/** POST /api/keys response */
export interface CreateKeyResult {
  clientId: string;
  apiKey: string;
}
