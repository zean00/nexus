import React from "react";
import { createRoot } from "react-dom/client";
const baseUrl = window.__NEXUS_TRUST_ADMIN_CONFIG__?.baseUrl ?? "/admin/trust";
function App() {
  const [summary, setSummary] = React.useState(null);
  const [policies, setPolicies] = React.useState([]);
  const [users, setUsers] = React.useState([]);
  const [events, setEvents] = React.useState([]);
  const [draft, setDraft] = React.useState({ agent_profile_id: "", require_linked_identity_for_execution: false, require_linked_identity_for_approval: false, require_recent_step_up_for_approval: false, allowed_approval_channels: "" });
  const load = React.useCallback(async () => {
    const [s, p, u, e] = await Promise.all([getJSON(`${baseUrl}/summary`), getJSON(`${baseUrl}/policies`), getJSON(`${baseUrl}/users`), getJSON(`${baseUrl}/events`)]);
    setSummary(s.data);
    setPolicies(p.data.items ?? []);
    setUsers(u.data.items ?? []);
    setEvents(e.data.items ?? []);
  }, []);
  React.useEffect(() => { void load(); }, [load]);
  async function submitPolicy(event) {
    event.preventDefault();
    await fetch(`${baseUrl}/policies/upsert`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ agent_profile_id: draft.agent_profile_id, require_linked_identity_for_execution: draft.require_linked_identity_for_execution, require_linked_identity_for_approval: draft.require_linked_identity_for_approval, require_recent_step_up_for_approval: draft.require_recent_step_up_for_approval, allowed_approval_channels: draft.allowed_approval_channels.split(",").map((item) => item.trim()).filter(Boolean) }) });
    setDraft({ agent_profile_id: "", require_linked_identity_for_execution: false, require_linked_identity_for_approval: false, require_recent_step_up_for_approval: false, allowed_approval_channels: "" });
    await load();
  }
  async function revoke(channelType, channelUserID) {
    await fetch(`${baseUrl}/links/revoke`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ channel_type: channelType, channel_user_id: channelUserID }) });
    await load();
  }
  return React.createElement("main", { className: "trust-admin" }, React.createElement("section", { className: "hero" }, React.createElement("p", { className: "kicker" }, "Trust Admin"), React.createElement("h1", null, "Identity, approval, and step-up policy")), React.createElement("section", { className: "panel" }, React.createElement("h2", null, "Summary"), React.createElement("pre", null, JSON.stringify(summary, null, 2))), React.createElement("section", { className: "grid" }, React.createElement("section", { className: "panel" }, React.createElement("h2", null, "Policies"), React.createElement("form", { onSubmit: submitPolicy, className: "form" }, React.createElement("input", { placeholder: "agent profile id", value: draft.agent_profile_id, onChange: (e) => setDraft({ ...draft, agent_profile_id: e.target.value }) }), React.createElement("label", null, React.createElement("input", { type: "checkbox", checked: draft.require_linked_identity_for_execution, onChange: (e) => setDraft({ ...draft, require_linked_identity_for_execution: e.target.checked }) }), " Require link for execution"), React.createElement("label", null, React.createElement("input", { type: "checkbox", checked: draft.require_linked_identity_for_approval, onChange: (e) => setDraft({ ...draft, require_linked_identity_for_approval: e.target.checked }) }), " Require link for approval"), React.createElement("label", null, React.createElement("input", { type: "checkbox", checked: draft.require_recent_step_up_for_approval, onChange: (e) => setDraft({ ...draft, require_recent_step_up_for_approval: e.target.checked }) }), " Require recent step-up"), React.createElement("input", { placeholder: "allowed approval channels, comma-separated", value: draft.allowed_approval_channels, onChange: (e) => setDraft({ ...draft, allowed_approval_channels: e.target.value }) }), React.createElement("button", { type: "submit" }, "Save policy")), React.createElement("pre", null, JSON.stringify(policies, null, 2))), React.createElement("section", { className: "panel" }, React.createElement("h2", null, "Users and links"), users.map((entry) => React.createElement("div", { key: entry.user.id, className: "user" }, React.createElement("strong", null, entry.user.primary_email), React.createElement("ul", null, (entry.linked_identities ?? []).map((identity) => React.createElement("li", { key: `${identity.channel_type}:${identity.channel_user_id}` }, `${identity.channel_type}:${identity.channel_user_id}`, React.createElement("button", { onClick: () => revoke(identity.channel_type, identity.channel_user_id) }, "Revoke")))))))), React.createElement("section", { className: "panel" }, React.createElement("h2", null, "Recent trust events"), React.createElement("pre", null, JSON.stringify(events, null, 2))));
}
async function getJSON(url) {
  const response = await fetch(url, { credentials: "same-origin" });
  if (!response.ok)
    throw new Error(await response.text());
  return response.json();
}
createRoot(document.getElementById("app")).render(React.createElement(App));

