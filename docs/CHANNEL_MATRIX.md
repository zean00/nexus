# Channel Behavior and Compatibility Matrix

This matrix reflects the implemented behavior in the current codebase.

## How To Use This Page

Use this document to answer two questions:

1. what the code supports today
2. which channel is the right fit for a specific workflow

There is no single best channel for every case. Nexus deliberately keeps one canonical backend while allowing channel UX to differ.

## Channel Summary

Implemented channels:

- Slack
- Telegram
- WhatsApp
- Email
- Webchat

If you are evaluating the project for the first time, start with Webchat. It exposes the broadest set of runtime behavior with the least provider friction.

## End-to-End Behavior Matrix

| Capability | Slack | Telegram | WhatsApp | Email | Webchat |
| --- | --- | --- | --- | --- | --- |
| Inbound verification | HMAC signature | Secret token | Verify token + HMAC signature | Secret or timestamped HMAC | First-party session |
| Inbound text | Yes | Yes | Yes | Yes | Yes |
| Inbound interactive await response | Yes | Yes | Yes | Yes | Yes |
| Inbound artifacts | Yes | No native file ingest in current adapter | Yes | Yes | Yes |
| Outbound status message | Yes | Yes | Yes | Yes | Persisted state + webchat adapter delivery |
| Outbound await prompt | Buttons | Inline keyboard | Buttons or text fallback | Email instructions | Native UI/API |
| Outbound artifacts | Yes | Yes | Yes, only public `http(s)` URLs | Yes | Yes |
| Partial streaming exposed to user | No | No | No | No | Yes |
| Identity linking path | Yes | Yes | Yes | Yes | Native first-party identity plus linking |
| Session commands | No | Yes | No | No | UI/API actions instead |

## UX Fidelity Matrix

| UX Area | Slack | Telegram | WhatsApp | Email | Webchat |
| --- | --- | --- | --- | --- | --- |
| Native interactive choices | Strong | Strong | Moderate | Weak | Strong |
| Artifact UX | Strong | Moderate | Moderate | Strong | Strong |
| Real-time updates | Replace-style status updates | Replace/edit-style status updates | Message sends only | Async email thread | SSE timeline |
| Rich artifact preview | Provider-native | Provider-native/basic | Provider-native/basic | Attachment-based | Inline image/audio/video |
| Best channel for ongoing conversations | Good | Good | Acceptable | Weak | Best |

## Channel-by-Channel Notes

### Slack

Strengths:

- strong webhook verification
- button-based await UX
- artifact upload support
- replace-style status rendering

Current notes:

- partial run events are not surfaced to Slack users
- OpenCode bridged structured awaits may be blocked and rendered as a failure explanation

### Telegram

Strengths:

- inline keyboard await UX
- explicit session commands in the current codebase
- document upload support for outbound artifacts

Current notes:

- the current inbound adapter focuses on text and callback updates
- Telegram is the only channel with built-in session control commands today

### WhatsApp

Strengths:

- text, interactive replies, and media ingress
- button-based awaits for small choice sets
- public-link outbound media support

Current notes:

- outbound artifacts require a reachable `http(s)` URL
- if there are more than three choices, the await UX degrades to text instructions
- partial streaming is not surfaced

### Email

Strengths:

- good fit for asynchronous approvals and long-form output
- attachment ingress and egress
- easy audit trail through threads

Current notes:

- await UX is inherently weaker than chat channels
- users respond by replying and keeping the await marker in the subject
- best treated as asynchronous transport, not a live runtime surface

### Webchat

Strengths:

- first-party authentication and identity model
- SSE updates
- true incremental output
- uploads plus authenticated artifact access
- configurable interaction visibility

Current notes:

- webchat is the most complete UX surface in the repository
- artifacts render inline for image/audio/video and as downloadable rows otherwise

## Artifact Matrix

| Artifact Behavior | Slack | Telegram | WhatsApp | Email | Webchat |
| --- | --- | --- | --- | --- | --- |
| Inbound artifact parsing | File metadata | No current file parsing | Image / document / audio metadata | Attachment metadata | Browser uploads |
| Inbound artifact hydration | Provider download | N/A | Provider media download | Base64 decode | Direct save to object store |
| Outbound artifact delivery | Upload send | Document send | Media send from public URL | Email attachment | Authenticated web delivery |
| Inline artifact preview | Provider-dependent | Provider-dependent | Provider-dependent | Client-dependent | Yes |

## Await / Approval Matrix

| Await Capability | Slack | Telegram | WhatsApp | Email | Webchat |
| --- | --- | --- | --- | --- | --- |
| Structured choice rendering | Yes | Yes | Yes, with limits | Text fallback | Yes |
| Arbitrary reply resume | Via mapped button choice payload | Via callback payload | Via button/list reply or text fallback | Email body reply | Native UI/API |
| Actor binding | Provider user ID | Provider user ID | Provider phone/user ID | Sender email | Auth session identity |

## Streaming Matrix

| Streaming Behavior | Slack | Telegram | WhatsApp | Email | Webchat |
| --- | --- | --- | --- | --- | --- |
| Worker persists incremental output | Yes | Yes | Yes | Yes | Yes |
| Renderer exposes partial output | No | No | No | No | No, but UI reads persisted partial state directly |
| User sees incremental output | No | No | No | No | Yes |

The distinction matters:

- the worker stores partial output in one canonical message row
- renderers for chat/email channels intentionally suppress partial sends
- webchat shows the growing assistant message from persisted state plus SSE refresh

## Compatibility Caveats

### OpenCode bridge vs native awaits

For channels that rely on structured human approval UX:

- native ACP backends are the best fit
- OpenCode bridge mode may warn or block structured await flows instead of leaving a broken pending interaction

### Artifact transport asymmetry

Artifact support is intentionally asymmetric:

- Slack, Telegram, and email can upload local/file-backed artifacts
- WhatsApp requires a public URL for outbound media sends
- webchat reads artifacts through the authenticated artifact endpoint

Those differences are structural, not accidental. Some providers simply expose a richer transport model than others.

## Recommended Channel Choices

| Use Case | Best Channel |
| --- | --- |
| Local development and debugging | Webchat |
| Rich first-party UX | Webchat |
| Team approvals in chat | Slack or Telegram |
| Mobile async interaction | WhatsApp |
| Long-form async review trail | Email |

## Related Docs

- [Features](./FEATURES.md)
- [Supported ACP Protocols and Bridges](./ACP_PROTOCOL.md)
- [Extending Nexus With a New Channel](./EXTENDING_CHANNELS.md)
