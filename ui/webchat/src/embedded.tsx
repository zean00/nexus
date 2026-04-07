import React from "react";
import { createRoot } from "react-dom/client";
import { WebChat } from "./react";
import type { EmbeddedWebChatConfig } from "./types";
import "./styles.css";

declare global {
  interface Window {
    __NEXUS_WEBCHAT_CONFIG__?: EmbeddedWebChatConfig;
  }
}

const config = window.__NEXUS_WEBCHAT_CONFIG__ ?? {};
const rootElement = document.getElementById("app");

if (rootElement) {
  createRoot(rootElement).render(
    <React.StrictMode>
      <WebChat
        baseUrl={config.baseUrl}
        labels={config.labels}
        theme={config.theme}
        features={config.features}
      />
    </React.StrictMode>
  );
}
