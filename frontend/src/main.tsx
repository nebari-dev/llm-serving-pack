import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { initKeycloak } from "@/auth/keycloak";
import { queryClient } from "@/lib/queryClient";
import { ThemeProvider } from "@/providers/ThemeProvider";

import App from "./App.tsx";

import "./index.css";

// Authenticate before rendering. initKeycloak() loads /config.json and runs the
// Keycloak login-required flow, only resolving once the user is signed in (or
// immediately, via the dev/E2E bypass).
await initKeycloak();

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Root element not found");
}

createRoot(rootElement).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <App />
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
