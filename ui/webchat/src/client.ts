import type {
  BootstrapData,
  IdentityProfileData,
  WebChatClientConfig,
  WebChatEventsPayload
} from "./types";

export class WebChatClient {
  private readonly baseUrl: string;
  private csrfToken = "";

  constructor(config: WebChatClientConfig = {}) {
    this.baseUrl = normalizeBaseUrl(config.baseUrl ?? "");
  }

  async requestAuth(email: string): Promise<void> {
    await this.requestJSON("/auth/request", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email })
    });
  }

  async verifyOTP(email: string, code: string): Promise<void> {
    await this.requestJSON("/auth/verify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, code })
    });
  }

  async logout(csrfToken?: string): Promise<void> {
    await this.requestJSON("/auth/logout", {
      method: "POST",
      headers: this.csrfHeaders(csrfToken)
    });
    this.csrfToken = "";
  }

  async bootstrap(): Promise<BootstrapData> {
    const payload = await this.requestJSON<{ data: BootstrapData }>("/bootstrap", {
      method: "GET"
    });
    this.csrfToken = payload.data.csrf_token;
    return payload.data;
  }

  async getHistory(limit = 100): Promise<WebChatEventsPayload> {
    const query = new URLSearchParams({ limit: String(limit) });
    const payload = await this.requestJSON<{ data: WebChatEventsPayload }>(`/history?${query.toString()}`, {
      method: "GET"
    });
    return payload.data;
  }

  async getIdentityProfile(): Promise<IdentityProfileData> {
    const payload = await this.requestJSON<{ data: IdentityProfileData }>("/identity/profile", {
      method: "GET"
    });
    return payload.data;
  }

  async updatePhone(phone: string): Promise<IdentityProfileData> {
    const payload = await this.requestJSON<{ data: IdentityProfileData }>("/identity/phone", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...this.csrfHeaders()
      },
      body: JSON.stringify({ phone })
    });
    return payload.data;
  }

  async deletePhone(): Promise<void> {
    await this.requestJSON("/identity/phone/delete", {
      method: "POST",
      headers: this.csrfHeaders()
    });
  }

  async sendMessage(input: { text: string; files?: File[] }): Promise<void> {
    const body = new FormData();
    if (input.text) {
      body.set("text", input.text);
    }
    for (const file of input.files ?? []) {
      body.append("files", file);
    }
    await this.requestJSON("/messages", {
      method: "POST",
      headers: this.csrfHeaders(),
      body
    });
  }

  async respondToAwait(input: { awaitId: string; reply: string }): Promise<void> {
    await this.requestJSON("/awaits/respond", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...this.csrfHeaders()
      },
      body: JSON.stringify({
        await_id: input.awaitId,
        reply: input.reply
      })
    });
  }

  async newChat(): Promise<void> {
    await this.requestJSON("/chats/new", {
      method: "POST",
      headers: this.csrfHeaders()
    });
  }

  async closeChat(): Promise<void> {
    await this.requestJSON("/chats/close", {
      method: "POST",
      headers: this.csrfHeaders()
    });
  }

  subscribe(onMessage: (payload: WebChatEventsPayload) => void): () => void {
    const source = new EventSource(this.url("/events"), { withCredentials: true });
    source.onmessage = (event) => {
      const payload = JSON.parse(event.data) as WebChatEventsPayload;
      onMessage(payload);
    };
    return () => source.close();
  }

  artifactURL(artifactID: string): string {
    return this.url(`/artifacts/${encodeURIComponent(artifactID)}`);
  }

  private csrfHeaders(override?: string): Record<string, string> {
    const token = override ?? this.csrfToken;
    return token ? { "X-CSRF-Token": token } : {};
  }

  private async requestJSON<T>(path: string, init: RequestInit): Promise<T> {
    const response = await fetch(this.url(path), {
      credentials: "same-origin",
      ...init
    });
    if (!response.ok) {
      throw new Error(await response.text() || `request failed with ${response.status}`);
    }
    return response.json() as Promise<T>;
  }

  private url(path: string): string {
    return `${this.baseUrl}${path.startsWith("/") ? path : `/${path}`}`;
  }
}

function normalizeBaseUrl(baseUrl: string): string {
  const value = baseUrl.trim();
  if (value === "" || value === "/webchat") {
    return "/webchat";
  }
  return value.endsWith("/") ? value.slice(0, -1) : value;
}

export function createWebChatClient(config: WebChatClientConfig = {}): WebChatClient {
  return new WebChatClient(config);
}
