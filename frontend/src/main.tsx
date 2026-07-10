import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { getAppConfig } from "@/app/config";
import { initKeycloak } from "@/auth/keycloak";
import { queryClient } from "@/lib/queryClient";
import { ThemeProvider } from "@/providers/ThemeProvider";

import App from "./App.tsx";

import "./index.css";

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Root element not found");
}

// Renders a plain, dependency-free message so a bootstrap failure (typically a
// malformed or unreachable /config.json) shows something actionable instead of
// a blank white page.
function renderBootstrapError(container: HTMLElement, message: string) {
  container.textContent = "";
  const wrapper = document.createElement("div");
  wrapper.setAttribute("role", "alert");
  wrapper.style.cssText =
    "max-width:40rem;margin:4rem auto;padding:0 1.5rem;font-family:system-ui,sans-serif;line-height:1.5";
  const heading = document.createElement("h1");
  heading.textContent = "Unable to start";
  const detail = document.createElement("p");
  detail.textContent = message;
  wrapper.append(heading, detail);
  container.append(wrapper);
}

// Authenticate before rendering. initKeycloak() loads /config.json and runs the
// Keycloak login-required flow, only resolving once the user is signed in (or
// immediately, via the dev/E2E bypass).
try {
  await initKeycloak();
} catch (err) {
  renderBootstrapError(
    rootElement,
    "The app could not load its runtime configuration or reach the login service. Check that /config.json is valid and Keycloak is reachable, then reload.",
  );
  throw err;
}

// Optional page-title override from /config.json (frontend.title in the chart).
const configuredTitle = getAppConfig()?.title;
if (configuredTitle) {
  document.title = configuredTitle;
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
