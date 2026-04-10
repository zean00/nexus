import React, { createContext, useContext, useEffect, useMemo, useState } from "react";
import { WebChatClient, createWebChatClient } from "./client";
import type {
  Artifact,
  IdentityProfileData,
  WebChatActivity,
  WebChatFeatures,
  WebChatInteractionVisibility,
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
  interactionVisibility?: WebChatInteractionVisibility;
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
  phoneLabel: "Phone",
  savePhone: "Save phone",
  removePhone: "Remove phone",
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
    () => props.client ?? providedClient ?? createWebChatClient({ baseUrl: props.baseUrl, interactionVisibility: props.interactionVisibility }),
    [props.baseUrl, props.client, props.interactionVisibility, providedClient]
  );
  const labels = { ...defaultLabels, ...props.labels };
  const features = { ...defaultFeatures, ...props.features };

  const [authenticated, setAuthenticated] = useState(false);
  const [email, setEmail] = useState("");
  const [items, setItems] = useState<WebChatItem[]>([]);
  const [serverVisibilityMode, setServerVisibilityMode] = useState<WebChatInteractionVisibility>(
    normalizeVisibilityMode(client.interactionVisibility ?? props.interactionVisibility ?? "full")
  );
  const [activity, setActivity] = useState<WebChatActivity | undefined>(undefined);
  const [optimisticActivity, setOptimisticActivity] = useState<WebChatActivity | undefined>(undefined);
  const [activityLabel, setActivityLabel] = useState("");
  const [requestEmail, setRequestEmail] = useState("");
  const [verifyEmail, setVerifyEmail] = useState("");
  const [verifyCode, setVerifyCode] = useState("");
  const [status, setStatus] = useState("");
  const [sendStatus, setSendStatus] = useState("");
  const [messageText, setMessageText] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [phone, setPhone] = useState("");
  const [phoneVerified, setPhoneVerified] = useState(false);
  const [identityProfile, setIdentityProfile] = useState<IdentityProfileData | null>(null);

  useEffect(() => {
    let ignore = false;
    client.bootstrap().then((data) => {
      if (ignore) {
        return;
      }
      setAuthenticated(true);
      applyBootstrapData(data);
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
      applyTimelinePayload(payload);
    });
  }, [authenticated, client, features.sse]);

  const effectiveVisibilityMode = capVisibilityMode(
    serverVisibilityMode,
    props.interactionVisibility ?? client.interactionVisibility
  );
  const effectiveActivity = activity ?? optimisticActivity;
  const visibleItems = useMemo(
    () => filterVisibleItems(items, effectiveVisibilityMode, effectiveActivity),
    [effectiveActivity, effectiveVisibilityMode, items]
  );

  useEffect(() => {
    if (effectiveVisibilityMode === "full" || effectiveVisibilityMode === "off" || !effectiveActivity) {
      setActivityLabel("");
      return;
    }
    const timer = window.setTimeout(() => {
      setActivityLabel(activityLabelForMode(effectiveVisibilityMode, effectiveActivity));
    }, 800);
    return () => window.clearTimeout(timer);
  }, [effectiveActivity, effectiveVisibilityMode]);

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
      applyBootstrapData(data);
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
      setOptimisticActivity({ phase: "thinking", updated_at: new Date().toISOString() });
      await client.sendMessage({ text: messageText.trim(), files });
      setMessageText("");
      setFiles([]);
      setSendStatus(labels.sendSuccess);
      props.onMessageSent?.();
    } catch (error) {
      setOptimisticActivity(undefined);
      props.onError?.(asError(error));
      setSendStatus(labels.sendFailed);
    }
  }

  async function handleAwaitChoice(awaitId: string, choice: string) {
    try {
      setOptimisticActivity({ phase: "thinking", updated_at: new Date().toISOString() });
      await client.respondToAwait({ awaitId, reply: choice });
      props.onAwaitResolved?.(awaitId, choice);
    } catch (error) {
      setOptimisticActivity(undefined);
      props.onError?.(asError(error));
      setSendStatus(labels.sendFailed);
    }
  }

  async function handleNewChat() {
    try {
      await client.newChat();
      const history = await client.getHistory();
      applyTimelinePayload(history);
    } catch (error) {
      props.onError?.(asError(error));
    }
  }

  async function handleSavePhone(event: React.FormEvent) {
    event.preventDefault();
    try {
      const profile = await client.updatePhone(phone);
      setIdentityProfile(profile);
      setPhone(profile.primary_phone ?? "");
      setPhoneVerified(Boolean(profile.primary_phone_verified));
      setStatus("Phone saved.");
    } catch (error) {
      props.onError?.(asError(error));
      setStatus("Phone update failed.");
    }
  }

  async function handleRemovePhone() {
    try {
      await client.deletePhone();
      setPhone("");
      setPhoneVerified(false);
      const profile = await client.getIdentityProfile();
      setIdentityProfile(profile);
      setStatus("Phone removed.");
    } catch (error) {
      props.onError?.(asError(error));
      setStatus("Phone removal failed.");
    }
  }

  async function handleLogout() {
    try {
      await client.logout();
    } finally {
      setAuthenticated(false);
      setItems([]);
      setActivity(undefined);
      setOptimisticActivity(undefined);
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
      <section className="nexus-webchat-panel">
        <form className="nexus-webchat-identity-form" onSubmit={handleSavePhone}>
          <label>
            <span>{labels.phoneLabel}</span>
            <input value={phone} onChange={(event) => setPhone(event.target.value)} placeholder="+628123456789" />
          </label>
          <div className="nexus-webchat-actions">
            <button type="submit">{labels.savePhone}</button>
            {phone ? <button className="secondary" onClick={handleRemovePhone} type="button">{labels.removePhone}</button> : null}
          </div>
          <p className="nexus-webchat-status">
            {phone ? `${phoneVerified ? "Verified" : "Unverified"} phone on profile.` : "Optional phone helps with explicit Telegram or WhatsApp pairing."}
          </p>
          {identityProfile?.link_hints ? <pre className="nexus-webchat-hints">{JSON.stringify(identityProfile.link_hints, null, 2)}</pre> : null}
        </form>
      </section>
      <section className="nexus-webchat-panel nexus-webchat-timeline">
        {visibleItems.length === 0 ? <div className="nexus-webchat-empty">{labels.emptyTimeline}</div> : null}
        {visibleItems.map((item) => (
          <TimelineItem
            key={item.id}
            item={item}
            onAwaitChoice={handleAwaitChoice}
            resolveArtifactURL={(artifact) => client.artifactURL(artifact.id)}
          />
        ))}
        {activityLabel ? <div className="nexus-webchat-activity">{activityLabel}</div> : null}
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

  function applyBootstrapData(data: {
    email: string;
    items?: WebChatItem[];
    activity?: WebChatActivity;
    visibility_mode?: WebChatInteractionVisibility;
    primary_phone?: string;
    primary_phone_verified?: boolean;
  }) {
    setEmail(data.email);
    setItems(data.items ?? []);
    setActivity(data.activity);
    setOptimisticActivity(undefined);
    setServerVisibilityMode(normalizeVisibilityMode(data.visibility_mode ?? "full"));
    setPhone(data.primary_phone ?? "");
    setPhoneVerified(Boolean(data.primary_phone_verified));
  }

  function applyTimelinePayload(payload: {
    items?: WebChatItem[];
    activity?: WebChatActivity;
    visibility_mode?: WebChatInteractionVisibility;
  }) {
    setItems(payload.items ?? []);
    setActivity(payload.activity);
    setOptimisticActivity(undefined);
    setServerVisibilityMode(normalizeVisibilityMode(payload.visibility_mode ?? serverVisibilityMode));
  }
}

function TimelineItem(props: {
  item: WebChatItem;
  onAwaitChoice: (awaitId: string, choice: string) => void;
  resolveArtifactURL: (artifact: Artifact) => string;
}) {
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
          {props.item.artifacts.map((artifact) => (
            <ArtifactLine
              key={artifact.id}
              artifact={artifact}
              href={props.resolveArtifactURL(artifact)}
            />
          ))}
        </div>
      ) : null}
    </article>
  );
}

function ArtifactLine(props: { artifact: Artifact; href: string }) {
  const [previewFailed, setPreviewFailed] = useState(false);
  const mimeType = (props.artifact.mime_type ?? "").trim().toLowerCase();
  const kind = artifactPreviewKind(mimeType);
  const name = props.artifact.name || props.artifact.id;
  const meta = artifactMeta(props.artifact);
  if (!previewFailed && props.href && kind === "image") {
    return (
      <div className="nexus-webchat-artifact-card">
        <img
          alt={name}
          className="nexus-webchat-artifact-image"
          loading="lazy"
          onError={() => setPreviewFailed(true)}
          src={props.href}
        />
        <ArtifactFileRow artifact={props.artifact} href={props.href} meta={meta} />
      </div>
    );
  }
  if (!previewFailed && props.href && kind === "audio") {
    return (
      <div className="nexus-webchat-artifact-card">
        <audio className="nexus-webchat-artifact-audio" controls onError={() => setPreviewFailed(true)} src={props.href} />
        <ArtifactFileRow artifact={props.artifact} href={props.href} meta={meta} />
      </div>
    );
  }
  if (!previewFailed && props.href && kind === "video") {
    return (
      <div className="nexus-webchat-artifact-card">
        <video className="nexus-webchat-artifact-video" controls onError={() => setPreviewFailed(true)} src={props.href} />
        <ArtifactFileRow artifact={props.artifact} href={props.href} meta={meta} />
      </div>
    );
  }
  return <ArtifactFileRow artifact={props.artifact} href={props.href} meta={meta} />;
}

function ArtifactFileRow(props: { artifact: Artifact; href: string; meta: string }) {
  const name = props.artifact.name || props.artifact.id;
  return (
    <div className="nexus-webchat-artifact-file">
      {props.href ? (
        <a href={props.href} target="_blank" rel="noreferrer">
          {name}
        </a>
      ) : (
        <span>{name}</span>
      )}
      {props.meta ? <span>{props.meta}</span> : null}
    </div>
  );
}

function artifactPreviewKind(mimeType: string): "image" | "audio" | "video" | "file" {
  if (mimeType.startsWith("image/")) {
    return "image";
  }
  if (mimeType.startsWith("audio/")) {
    return "audio";
  }
  if (mimeType.startsWith("video/")) {
    return "video";
  }
  return "file";
}

function artifactMeta(artifact: Artifact): string {
  const parts: string[] = [];
  if (artifact.mime_type) {
    parts.push(artifact.mime_type);
  }
  if (typeof artifact.size_bytes === "number" && artifact.size_bytes > 0) {
    parts.push(formatBytes(artifact.size_bytes));
  }
  return parts.join(" ");
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "";
  }
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const fixed = size >= 10 || unitIndex === 0 ? 0 : 1;
  return `${size.toFixed(fixed)} ${units[unitIndex]}`;
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

function normalizeVisibilityMode(value: string): WebChatInteractionVisibility {
  switch ((value ?? "").trim().toLowerCase()) {
    case "simple":
    case "minimal":
    case "off":
      return value.trim().toLowerCase() as WebChatInteractionVisibility;
    case "full":
    default:
      return "full";
  }
}

function capVisibilityMode(serverMode: WebChatInteractionVisibility, requested?: WebChatInteractionVisibility): WebChatInteractionVisibility {
  const requestedMode = normalizeVisibilityMode(requested ?? serverMode);
  return visibilityRank(requestedMode) >= visibilityRank(serverMode) ? requestedMode : serverMode;
}

function visibilityRank(mode: WebChatInteractionVisibility): number {
  switch (mode) {
    case "simple":
      return 1;
    case "minimal":
      return 2;
    case "off":
      return 3;
    case "full":
    default:
      return 0;
  }
}

function filterVisibleItems(
  items: WebChatItem[],
  mode: WebChatInteractionVisibility,
  activity?: WebChatActivity
): WebChatItem[] {
  if ((mode !== "minimal" && mode !== "off") || !activity) {
    return items;
  }
  return items.filter((item) => !(item.type === "message" && item.role === "assistant" && item.partial));
}

function activityLabelForMode(mode: WebChatInteractionVisibility, activity: WebChatActivity): string {
  if (mode === "minimal") {
    return "Typing...";
  }
  if (mode !== "simple") {
    return "";
  }
  switch (activity.phase) {
    case "typing":
      return "Typing...";
    case "working":
      return "Working...";
    case "thinking":
    default:
      return "Thinking...";
  }
}

function asError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value));
}
