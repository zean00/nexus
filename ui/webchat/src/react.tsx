import React, { createContext, useContext, useEffect, useMemo, useState } from "react";
import { WebChatClient, createWebChatClient } from "./client";
import type {
  Artifact,
  WebChatFeatures,
  WebChatItem,
  WebChatLabels,
  WebChatTheme
} from "./types";

const clientContext = createContext<WebChatClient | null>(null);

export function WebChatProvider(props: { client: WebChatClient; children: React.ReactNode }) {
  return <clientContext.Provider value={props.client}>{props.children}</clientContext.Provider>;
}

export function useWebChatClient() {
  const client = useContext(clientContext);
  if (!client) {
    throw new Error("WebChatProvider is required");
  }
  return client;
}

type WebChatProps = {
  client?: WebChatClient;
  baseUrl?: string;
  labels?: WebChatLabels;
  theme?: WebChatTheme;
  features?: WebChatFeatures;
  className?: string;
  style?: React.CSSProperties;
  onAuthChange?: (authenticated: boolean) => void;
  onMessageSent?: () => void;
  onAwaitResolved?: (awaitId: string, choice: string) => void;
  onError?: (error: Error) => void;
};

const defaultLabels: Required<WebChatLabels> = {
  title: "Nexus Web Chat",
  subtitle: "Use your email to receive a code or magic link.",
  emailLabel: "Email",
  otpLabel: "Code",
  requestCode: "Send code",
  verifyCode: "Verify code",
  authHelp: "Check your email for a code or link.",
  authSent: "Check your email for a code or link.",
  authFailed: "Verification failed.",
  timelineTitle: "Conversation",
  newChat: "New chat",
  logout: "Log out",
  composerPlaceholder: "Send a message",
  send: "Send",
  sendSuccess: "Sent.",
  sendFailed: "Send failed.",
  signedInAsPrefix: "Signed in as",
  emptyTimeline: "Your conversation will appear here.",
  unauthorized: "Sign in to continue."
};

const defaultFeatures: Required<WebChatFeatures> = {
  auth: true,
  uploads: true,
  newChat: true,
  logout: true,
  sse: true
};

export function WebChat(props: WebChatProps) {
  const providedClient = useContext(clientContext);
  const client = useMemo(
    () => props.client ?? providedClient ?? createWebChatClient({ baseUrl: props.baseUrl }),
    [props.baseUrl, props.client, providedClient]
  );
  const labels = { ...defaultLabels, ...props.labels };
  const features = { ...defaultFeatures, ...props.features };

  const [authenticated, setAuthenticated] = useState(false);
  const [email, setEmail] = useState("");
  const [items, setItems] = useState<WebChatItem[]>([]);
  const [requestEmail, setRequestEmail] = useState("");
  const [verifyEmail, setVerifyEmail] = useState("");
  const [verifyCode, setVerifyCode] = useState("");
  const [status, setStatus] = useState("");
  const [sendStatus, setSendStatus] = useState("");
  const [messageText, setMessageText] = useState("");
  const [files, setFiles] = useState<File[]>([]);

  useEffect(() => {
    let ignore = false;
    client.bootstrap().then((data) => {
      if (ignore) {
        return;
      }
      setAuthenticated(true);
      setEmail(data.email);
      setItems(data.items ?? []);
      props.onAuthChange?.(true);
    }).catch((error: Error) => {
      if (ignore) {
        return;
      }
      setAuthenticated(false);
      setItems([]);
      props.onAuthChange?.(false);
      if (features.auth === false) {
        props.onError?.(error);
      }
    });
    return () => {
      ignore = true;
    };
  }, [client, features.auth, props]);

  useEffect(() => {
    if (!authenticated || !features.sse) {
      return;
    }
    return client.subscribe((payload) => {
      setItems(payload.items ?? []);
    });
  }, [authenticated, client, features.sse]);

  const themeStyle = buildThemeStyle(props.theme);
  const rootClassName = props.className ? `nexus-webchat-shell ${props.className}` : "nexus-webchat-shell";

  async function handleRequestAuth(event: React.FormEvent) {
    event.preventDefault();
    try {
      await client.requestAuth(requestEmail);
      setStatus(labels.authSent);
    } catch (error) {
      props.onError?.(asError(error));
      setStatus(labels.sendFailed);
    }
  }

  async function handleVerifyAuth(event: React.FormEvent) {
    event.preventDefault();
    try {
      await client.verifyOTP(verifyEmail, verifyCode);
      const data = await client.bootstrap();
      setAuthenticated(true);
      setEmail(data.email);
      setItems(data.items ?? []);
      setStatus("");
      setVerifyCode("");
      props.onAuthChange?.(true);
    } catch (error) {
      props.onError?.(asError(error));
      setStatus(labels.authFailed);
    }
  }

  async function handleSendMessage(event: React.FormEvent) {
    event.preventDefault();
    try {
      await client.sendMessage({ text: messageText.trim(), files });
      setMessageText("");
      setFiles([]);
      setSendStatus(labels.sendSuccess);
      props.onMessageSent?.();
    } catch (error) {
      props.onError?.(asError(error));
      setSendStatus(labels.sendFailed);
    }
  }

  async function handleAwaitChoice(awaitId: string, choice: string) {
    try {
      await client.respondToAwait({ awaitId, reply: choice });
      props.onAwaitResolved?.(awaitId, choice);
    } catch (error) {
      props.onError?.(asError(error));
      setSendStatus(labels.sendFailed);
    }
  }

  async function handleNewChat() {
    try {
      await client.newChat();
      const history = await client.getHistory();
      setItems(history.items ?? []);
    } catch (error) {
      props.onError?.(asError(error));
    }
  }

  async function handleLogout() {
    try {
      await client.logout();
    } finally {
      setAuthenticated(false);
      setItems([]);
      setEmail("");
      props.onAuthChange?.(false);
    }
  }

  if (!authenticated) {
    return (
      <div className={rootClassName} style={{ ...themeStyle, ...props.style }}>
        <div className="nexus-webchat-auth">
          <div className="nexus-webchat-brand">
            <p className="nexus-webchat-kicker">Reusable chat surface</p>
            <h1>{labels.title}</h1>
            <p>{labels.subtitle}</p>
          </div>
          <div className="nexus-webchat-auth-grid">
            <form className="nexus-webchat-panel" onSubmit={handleRequestAuth}>
              <label>
                <span>{labels.emailLabel}</span>
                <input type="email" value={requestEmail} onChange={(event) => setRequestEmail(event.target.value)} required />
              </label>
              <button type="submit">{labels.requestCode}</button>
            </form>
            <form className="nexus-webchat-panel" onSubmit={handleVerifyAuth}>
              <label>
                <span>{labels.emailLabel}</span>
                <input type="email" value={verifyEmail} onChange={(event) => setVerifyEmail(event.target.value)} required />
              </label>
              <label>
                <span>{labels.otpLabel}</span>
                <input value={verifyCode} onChange={(event) => setVerifyCode(event.target.value)} required />
              </label>
              <button type="submit">{labels.verifyCode}</button>
            </form>
          </div>
          <p className="nexus-webchat-status">{status || labels.authHelp}</p>
        </div>
      </div>
    );
  }

  return (
    <div className={rootClassName} style={{ ...themeStyle, ...props.style }}>
      <header className="nexus-webchat-header">
        <div>
          <p className="nexus-webchat-kicker">{labels.timelineTitle}</p>
          <h1>{labels.title}</h1>
          <p>{labels.signedInAsPrefix} {email}</p>
        </div>
        <div className="nexus-webchat-actions">
          {features.newChat ? <button className="secondary" onClick={handleNewChat} type="button">{labels.newChat}</button> : null}
          {features.logout ? <button onClick={handleLogout} type="button">{labels.logout}</button> : null}
        </div>
      </header>
      <section className="nexus-webchat-panel nexus-webchat-timeline">
        {items.length === 0 ? <div className="nexus-webchat-empty">{labels.emptyTimeline}</div> : null}
        {items.map((item) => <TimelineItem key={item.id} item={item} onAwaitChoice={handleAwaitChoice} />)}
      </section>
      <form className="nexus-webchat-panel nexus-webchat-composer" onSubmit={handleSendMessage}>
        <textarea
          placeholder={labels.composerPlaceholder}
          value={messageText}
          onChange={(event) => setMessageText(event.target.value)}
        />
        {features.uploads ? (
          <input
            type="file"
            multiple
            onChange={(event) => setFiles(Array.from(event.target.files ?? []))}
          />
        ) : null}
        <div className="nexus-webchat-actions">
          <button type="submit">{labels.send}</button>
        </div>
        <p className="nexus-webchat-status">{sendStatus}</p>
      </form>
    </div>
  );
}

function TimelineItem(props: { item: WebChatItem; onAwaitChoice: (awaitId: string, choice: string) => void }) {
  const role = props.item.role ?? "assistant";
  const awaitID = props.item.await_id ?? "";
  return (
    <article className={`nexus-webchat-item ${role}`}>
      <div className="nexus-webchat-item-body">
        {props.item.text || props.item.status || props.item.type}
      </div>
      {props.item.choices && props.item.choices.length > 0 && awaitID ? (
        <div className="nexus-webchat-actions">
          {props.item.choices.map((choice) => (
            <button key={choice.id} type="button" onClick={() => props.onAwaitChoice(awaitID, choice.id)}>
              {choice.label}
            </button>
          ))}
        </div>
      ) : null}
      {props.item.artifacts && props.item.artifacts.length > 0 ? (
        <div className="nexus-webchat-artifacts">
          {props.item.artifacts.map((artifact) => <ArtifactLine key={artifact.id} artifact={artifact} />)}
        </div>
      ) : null}
    </article>
  );
}

function ArtifactLine(props: { artifact: Artifact }) {
  return (
    <div className="nexus-webchat-artifact">
      <span>{props.artifact.name || props.artifact.id}</span>
      {props.artifact.mime_type ? <span>{props.artifact.mime_type}</span> : null}
    </div>
  );
}

function buildThemeStyle(theme?: WebChatTheme): React.CSSProperties {
  return {
    "--nexus-webchat-accent": theme?.accent,
    "--nexus-webchat-accent-contrast": theme?.accentContrast,
    "--nexus-webchat-ink": theme?.ink,
    "--nexus-webchat-muted": theme?.muted,
    "--nexus-webchat-background": theme?.background,
    "--nexus-webchat-panel": theme?.panel,
    "--nexus-webchat-border": theme?.border,
    "--nexus-webchat-user": theme?.surfaceUser,
    "--nexus-webchat-assistant": theme?.surfaceAssistant,
    "--nexus-webchat-radius": theme?.radius,
    "--nexus-webchat-shadow": theme?.shadow,
    "--nexus-webchat-font": theme?.fontFamily,
    "--nexus-webchat-gap": theme?.compact ? "0.8rem" : "1.25rem",
    "--nexus-webchat-pad": theme?.compact ? "0.95rem" : "1.35rem"
  } as React.CSSProperties;
}

function asError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value));
}
