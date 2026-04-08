import React from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

declare global {
  interface Window {
    __NEXUS_TRUST_ADMIN_CONFIG__?: { baseUrl?: string };
  }
}

const baseUrl = window.__NEXUS_TRUST_ADMIN_CONFIG__?.baseUrl ?? "/admin/trust";

function App() {
  const [summary, setSummary] = React.useState<any>(null);
  const [policies, setPolicies] = React.useState<any[]>([]);
  const [users, setUsers] = React.useState<any[]>([]);
  const [events, setEvents] = React.useState<any[]>([]);
  const [draft, setDraft] = React.useState({
    agent_profile_id: "",
    require_linked_identity_for_execution: false,
    require_linked_identity_for_approval: false,
    require_recent_step_up_for_approval: false,
    allowed_approval_channels: ""
  });

  const load = React.useCallback(async () => {
    const [s, p, u, e] = await Promise.all([
      getJSON(`${baseUrl}/summary`),
      getJSON(`${baseUrl}/policies`),
      getJSON(`${baseUrl}/users`),
      getJSON(`${baseUrl}/events`)
    ]);
    setSummary(s.data);
    setPolicies(p.data.items ?? []);
    setUsers(u.data.items ?? []);
    setEvents(e.data.items ?? []);
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function submitPolicy(event: React.FormEvent) {
    event.preventDefault();
    await fetch(`${baseUrl}/policies/upsert`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        agent_profile_id: draft.agent_profile_id,
        require_linked_identity_for_execution: draft.require_linked_identity_for_execution,
        require_linked_identity_for_approval: draft.require_linked_identity_for_approval,
        require_recent_step_up_for_approval: draft.require_recent_step_up_for_approval,
        allowed_approval_channels: draft.allowed_approval_channels.split(",").map((item) => item.trim()).filter(Boolean)
      })
    });
    setDraft({
      agent_profile_id: "",
      require_linked_identity_for_execution: false,
      require_linked_identity_for_approval: false,
      require_recent_step_up_for_approval: false,
      allowed_approval_channels: ""
    });
    await load();
  }

  async function revoke(channelType: string, channelUserID: string) {
    await fetch(`${baseUrl}/links/revoke`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel_type: channelType, channel_user_id: channelUserID })
    });
    await load();
  }

  return (
    <main className="trust-admin">
      <section className="hero">
        <p className="kicker">Trust Admin</p>
        <h1>Identity, approval, and step-up policy</h1>
      </section>
      <section className="panel">
        <h2>Summary</h2>
        <pre>{JSON.stringify(summary, null, 2)}</pre>
      </section>
      <section className="grid">
        <section className="panel">
          <h2>Policies</h2>
          <form onSubmit={submitPolicy} className="form">
            <input placeholder="agent profile id" value={draft.agent_profile_id} onChange={(e) => setDraft({ ...draft, agent_profile_id: e.target.value })} />
            <label><input type="checkbox" checked={draft.require_linked_identity_for_execution} onChange={(e) => setDraft({ ...draft, require_linked_identity_for_execution: e.target.checked })} /> Require link for execution</label>
            <label><input type="checkbox" checked={draft.require_linked_identity_for_approval} onChange={(e) => setDraft({ ...draft, require_linked_identity_for_approval: e.target.checked })} /> Require link for approval</label>
            <label><input type="checkbox" checked={draft.require_recent_step_up_for_approval} onChange={(e) => setDraft({ ...draft, require_recent_step_up_for_approval: e.target.checked })} /> Require recent step-up</label>
            <input placeholder="allowed approval channels, comma-separated" value={draft.allowed_approval_channels} onChange={(e) => setDraft({ ...draft, allowed_approval_channels: e.target.value })} />
            <button type="submit">Save policy</button>
          </form>
          <pre>{JSON.stringify(policies, null, 2)}</pre>
        </section>
        <section className="panel">
          <h2>Users and links</h2>
          {users.map((entry) => (
            <div key={entry.user.id} className="user">
              <strong>{entry.user.primary_email}</strong>
              {entry.user.primary_phone ? <div>{entry.user.primary_phone} {entry.user.primary_phone_verified ? "(verified)" : "(unverified)"}</div> : null}
              <ul>
                {(entry.linked_identities ?? []).map((identity: any) => (
                  <li key={`${identity.channel_type}:${identity.channel_user_id}`}>
                    {identity.channel_type}:{identity.channel_user_id}
                    <button onClick={() => revoke(identity.channel_type, identity.channel_user_id)}>Revoke</button>
                  </li>
                ))}
              </ul>
              {entry.link_hints ? <pre>{JSON.stringify(entry.link_hints, null, 2)}</pre> : null}
            </div>
          ))}
        </section>
      </section>
      <section className="panel">
        <h2>Recent trust events</h2>
        <pre>{JSON.stringify(events, null, 2)}</pre>
      </section>
    </main>
  );
}

async function getJSON(url: string) {
  const response = await fetch(url, { credentials: "same-origin" });
  if (!response.ok) {
    throw new Error(await response.text());
  }
  return response.json();
}

createRoot(document.getElementById("app")!).render(<App />);
