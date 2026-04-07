export type ChatItemType = "message" | "await";

export interface RenderChoice {
  id: string;
  label: string;
}

export interface Artifact {
  id: string;
  message_id?: string;
  name?: string;
  mime_type?: string;
  size_bytes?: number;
  sha256?: string;
  storage_uri?: string;
  source_url?: string;
}

export interface WebChatItem {
  id: string;
  type: ChatItemType | string;
  role?: string;
  text?: string;
  status?: string;
  await_id?: string;
  choices?: RenderChoice[];
  artifacts?: Artifact[];
  meta?: Record<string, string>;
}

export interface BootstrapData {
  email: string;
  session_id: string;
  csrf_token: string;
  items: WebChatItem[];
}

export interface WebChatEventsPayload {
  items: WebChatItem[];
}

export interface WebChatClientConfig {
  baseUrl?: string;
}

export interface WebChatLabels {
  title?: string;
  subtitle?: string;
  emailLabel?: string;
  otpLabel?: string;
  requestCode?: string;
  verifyCode?: string;
  authHelp?: string;
  authSent?: string;
  authFailed?: string;
  timelineTitle?: string;
  newChat?: string;
  logout?: string;
  composerPlaceholder?: string;
  send?: string;
  sendSuccess?: string;
  sendFailed?: string;
  signedInAsPrefix?: string;
  emptyTimeline?: string;
  unauthorized?: string;
}

export interface WebChatTheme {
  accent?: string;
  accentContrast?: string;
  ink?: string;
  muted?: string;
  background?: string;
  panel?: string;
  border?: string;
  surfaceUser?: string;
  surfaceAssistant?: string;
  radius?: string;
  shadow?: string;
  fontFamily?: string;
  compact?: boolean;
}

export interface WebChatFeatures {
  auth?: boolean;
  uploads?: boolean;
  newChat?: boolean;
  logout?: boolean;
  sse?: boolean;
}

export interface EmbeddedWebChatConfig {
  baseUrl?: string;
  labels?: WebChatLabels;
  theme?: WebChatTheme;
  features?: WebChatFeatures;
}
