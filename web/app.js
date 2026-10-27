(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
  const state = { data: null, page: "dashboard", editing: null, repoConn: null, poll: null };
  const weekday = ["Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"];
  const API_ENDPOINT = "api.cgi";
  const synoToken = new URLSearchParams(window.location.search).get("SynoToken") || "";

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function fmtDate(v) {
    if (!v) return "–";
    const d = new Date(v);
    if (isNaN(d)) return "–";
    return d.toLocaleString("de-DE", { dateStyle: "medium", timeStyle: "short" });
  }
  function fmtRelative(v) {
    if (!v) return "–";
    const d = new Date(v), diff = (Date.now() - d.getTime()) / 1000;
    if (diff < 0) return fmtDate(v);
    if (diff < 60) return "gerade eben";
    if (diff < 3600) return `vor ${Math.floor(diff / 60)} Min.`;
    if (diff < 86400) return `vor ${Math.floor(diff / 3600)} Std.`;
    return fmtDate(v);
  }
  function fmtElapsed(v) {
    if (!v) return "–";
    const diff = Math.max(0, (Date.now() - new Date(v).getTime()) / 1000);
    if (diff < 60) return `${Math.floor(diff)} Sek.`;
    if (diff < 3600) return `${Math.floor(diff / 60)} Min.`;
    return `${Math.floor(diff / 3600)} Std.`;
  }
  function fmtBytes(n) {
    if (!n) return "0 B";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
  }
  function toast(msg, kind = "") {
    const el = $("#toast");
    el.textContent = msg;
    el.className = `toast ${kind}`;
    clearTimeout(el._t);
    el._t = setTimeout(() => el.classList.add("hidden"), 4200);
  }
  async function api(path, opts = {}) {
    const headers = { "X-GitHub-Backup": "1", ...(synoToken ? { "X-SYNO-TOKEN": synoToken } : {}), ...(opts.body ? { "Content-Type": "application/json" } : {}), ...(opts.headers || {}) };
    const target = path.startsWith("/") ? path : `/${path}`;
    const url = `${API_ENDPOINT}?path=${encodeURIComponent(target)}`;
    const res = await fetch(url, { credentials: "same-origin", ...opts, headers });
    let data = null;
    try { data = await res.json(); } catch (_) {}
    if (res.status === 401 || res.status === 403) {
      showAuth(data?.error || "Nicht in DSM angemeldet.");
      throw new Error(data?.error || "Nicht angemeldet");
    }
    if (!res.ok) throw new Error(data?.error || `HTTP ${res.status}`);
    return data;
  }
  function showAuth(text) {
    $("#boot").classList.add("hidden");
    $("#app").classList.add("hidden");
    $("#auth").classList.remove("hidden");
    if (text) $("#authText").textContent = text;
  }
  function showApp(user) {
    $("#boot").classList.add("hidden");
    $("#auth").classList.add("hidden");
    $("#app").classList.remove("hidden");
    $("#userName").textContent = user || "Administrator";
  }

  async function init() {
    try {
      const s = await api("api/session");
      showApp(s.user);
      await loadState();
      startPolling();
    } catch (e) {
      if (!/Nicht in DSM|Nur DSM/i.test(e.message || "")) {
        showAuth(`GitHub Backup ist nicht erreichbar: ${e.message || "unbekannter Fehler"}`);
      }
    }
  }

  async function loadState() {
    try {
      const data = await api("api/state");
      state.data = data;
      render();
    } catch (e) {
      if (e.message && !/Nicht|angemeldet/i.test(e.message)) toast(e.message, "error");
    }
  }

  function startPolling() {
    if (state.poll) clearInterval(state.poll);
    state.poll = setInterval(loadState, 2000);
  }

  function render() {
    const d = state.data;
    if (!d) return;
    $("#version").textContent = "v1.1.2";
    $("#statRepos").textContent = d.stats?.repos ?? "–";
    $("#statWikis").textContent = `${d.stats?.wikis ?? 0} Wikis`;
    $("#statConns").textContent = d.connections?.length ?? 0;
    $("#statEnabled").textContent = `${d.connections?.filter(c => c.enabled).length ?? 0} aktiv`;
    $("#statLast").textContent = fmtRelative(d.last_run);
    $("#statLastStatus").textContent = statusLabel(d.last_status);
    $("#statLastStatus").style.color = d.last_status === "success" ? "var(--green)" : (d.last_status === "failed" || d.last_status === "completed_with_errors" ? "var(--red)" : "");
    $("#statNext").textContent = d.next_run ? fmtDate(d.next_run) : "–";
    $("#statSchedule").textContent = d.schedule?.enabled ? `${weekday[d.schedule.weekday]} ${pad(d.schedule.hour)}:${pad(d.schedule.minute)}` : "deaktiviert";
    renderRun(d.run);
    renderConnections(d.connections || [], $("#dashConnections"));
    renderConnections(d.connections || [], $("#connections"));
  }

  function pad(n) { return String(n).padStart(2, "0"); }
  function statusLabel(s) {
    if (!s) return "–";
    if (s === "idle") return "Bereit";
    if (s === "success") return "Erfolgreich";
    if (s === "completed_with_errors") return "Mit Fehlern";
    if (s === "running") return "Läuft";
    if (s === "empty") return "Leer";
    if (s === "failed" || s === "error") return "Fehlgeschlagen";
    return s;
  }
  function statusClass(s) {
    if (s === "success") return "badge success";
    if (s === "completed_with_errors" || s === "failed" || s === "error") return "badge error";
    if (s === "running") return "badge running";
    return "badge";
  }
  function renderRun(run) {
    const panel = $("#runPanel"), badge = $("#runBadge"), btn = $("#runBtn");
    if (!run) run = { status: "idle" };
    if (run.running) {
      panel.classList.remove("hidden");
      badge.className = "badge running";
      badge.textContent = "Backup läuft";
      btn.disabled = true;
      $("#runCurrent").textContent = run.current || "Vorbereitung …";
      const pct = run.total ? Math.min(100, Math.round((run.done / run.total) * 100)) : 0;
      $("#runBar").style.width = `${pct}%`;
      $("#runCount").textContent = `${run.done} / ${run.total}`;
      $("#runElapsed").textContent = run.started ? `seit ${fmtElapsed(run.started)}` : "";
    } else {
      panel.classList.add("hidden");
      btn.disabled = false;
      badge.className = statusClass(run.status || "idle");
      badge.textContent = statusLabel(run.status || "idle");
    }
  }

  function renderConnections(list, root) {
    if (!root) return;
    if (!list.length) {
      root.innerHTML = `<div class="empty">Noch keine Verbindung. Lege eine persönliche, Org- oder Classic-Verbindung an.</div>`;
      return;
    }
    root.innerHTML = list.map(c => `
      <article class="card conn-card">
        <div class="conn-top">
          <div>
            <p class="conn-title">${esc(c.label)}</p>
            <div class="conn-sub">${esc(kindLabel(c.kind))} · ${esc(c.owner || "–")}</div>
          </div>
          <span class="pill ${c.enabled ? (c.last_status === "success" ? "ok" : c.last_status === "error" ? "err" : "") : "off"}">${c.enabled ? esc(statusLabel(c.last_status || "bereit")) : "aus"}</span>
        </div>
        <div class="conn-sub">${c.mode === "all" ? "Alle Repos inkl. neuer" : `${c.selected_count} ausgewählt`} · letzter Lauf: ${esc(fmtRelative(c.last_run))}</div>
        <div class="conn-path">${esc(c.effective_path || "")}</div>
        <div class="conn-actions">
          <button class="btn primary" data-run="${esc(c.id)}">Sichern</button>
          <button class="btn" data-repos="${esc(c.id)}">Repos</button>
          <button class="btn" data-edit="${esc(c.id)}">Bearbeiten</button>
          <button class="btn" data-test="${esc(c.id)}">Testen</button>
        </div>
      </article>`).join("");
  }

  function kindLabel(k) {
    return k === "classic" ? "Classic" : k === "org" ? "Organisation" : "Persönlich";
  }

  function navigate(page) {
    state.page = page;
    $$(".page").forEach(p => p.classList.add("hidden"));
    $(`#page-${page}`)?.classList.remove("hidden");
    $$(".nav").forEach(b => b.classList.toggle("active", b.dataset.page === page));
    $("#pageTitle").textContent = page === "dashboard" ? "Übersicht" : page === "connections" ? "Verbindungen" : page === "logs" ? "Logs" : "Einstellungen";
    $("#pageSub").textContent = page === "dashboard" ? "Status und letzte Sicherung" : page === "connections" ? "GitHub-Konten und Organisationen" : page === "logs" ? "Alle Backup-Läufe" : "Ziel, Zeitplan und Optionen";
    if (page === "logs") loadLogs();
    if (page === "settings") fillSettings();
  }

  function connection(id) { return (state.data?.connections || []).find(c => c.id === id); }

  function openConn(id) {
    state.editing = id || null;
    const c = id ? connection(id) : null;
    $("#connTitle").textContent = c ? "Verbindung bearbeiten" : "Verbindung hinzufügen";
    $("#connKind").value = c?.kind || "classic";
    $("#connOwner").value = c?.owner || "";
    $("#connLabel").value = c?.label || "";
    $("#connToken").value = "";
    $("#connToken").placeholder = c ? "leer lassen = Token unverändert" : "ghp_… oder github_pat_…";
    $("#connMode").value = c?.mode || "all";
    $("#connPath").value = c?.path || "";
    $("#connEnabled").checked = c ? c.enabled : true;
    $("#connEnabledWrap").classList.toggle("hidden", !c);
    $("#connDelete").classList.toggle("hidden", !c);
    $("#ownerWrap").classList.toggle("hidden", $("#connKind").value !== "org");
    $("#connDialog").showModal();
  }

  async function saveConn(e) {
    e.preventDefault();
    const id = state.editing;
    const body = {
      kind: $("#connKind").value,
      owner: $("#connOwner").value.trim(),
      label: $("#connLabel").value.trim(),
      token: $("#connToken").value.trim(),
      mode: "all",
      path: $("#connPath").value.trim(),
    };
    try {
      if (id) {
        const old = connection(id);
        const chosen = $("#connMode").value;
        const payload = { label: body.label, path: body.path, enabled: $("#connEnabled").checked };
        // "selected" is only persisted through the repository picker, because
        // an empty selection would be normalized back to "all".
        if (chosen === "all" && old?.mode !== "all") payload.mode = "all";
        if (body.token) payload.token = body.token;
        await api(`api/connections/${id}`, { method: "PUT", body: JSON.stringify(payload) });
        $("#connDialog").close();
        toast("Verbindung gespeichert", "success");
        await loadState();
        if (chosen === "selected" && old?.mode !== "selected") openRepos(id);
      } else {
        const res = await api("api/connections", { method: "POST", body: JSON.stringify(body) });
        $("#connDialog").close();
        toast(`Verbindung angelegt (${res.repos} Repos sichtbar)`, "success");
        await loadState();
        if ($("#connMode").value === "selected") openRepos(res.id);
      }
    } catch (err) { toast(err.message, "error"); }
  }

  async function deleteConn() {
    const id = state.editing;
    if (!id || !confirm("Verbindung wirklich löschen? Die Backup-Daten bleiben erhalten.")) return;
    try {
      await api(`api/connections/${id}`, { method: "DELETE" });
      $("#connDialog").close();
      toast("Verbindung gelöscht", "success");
      await loadState();
    } catch (err) { toast(err.message, "error"); }
  }

  async function testConn(id) {
    try {
      const r = await api(`api/connections/${id}/test`, { method: "POST" });
      if (r.ok) toast(`Token OK · ${r.count} Repositories sichtbar`, "success");
      else toast(`Token-Test fehlgeschlagen: ${r.error}`, "error");
    } catch (err) { toast(err.message, "error"); }
  }

  async function openRepos(id) {
    state.repoConn = id;
    const c = connection(id);
    $("#repoTitle").textContent = `Repositories · ${c?.label || ""}`;
    $("#repoSearch").value = "";
    $("#repoAll").checked = c?.mode === "all";
    $("#repoList").innerHTML = `<div class="empty">Lade Repositories …</div>`;
    $("#repoDialog").showModal();
    try {
      const r = await api(`api/connections/${id}/repos`);
      state.repoData = r;
      renderRepoList();
    } catch (err) {
      $("#repoList").innerHTML = `<div class="empty">${esc(err.message)}</div>`;
    }
  }

  function renderRepoList() {
    const r = state.repoData;
    if (!r) return;
    const q = $("#repoSearch").value.toLowerCase().trim();
    const selected = new Set(r.selected || []);
    const all = $("#repoAll").checked;
    const repos = (r.repos || []).filter(x => !q || x.full_name.toLowerCase().includes(q));
    $("#repoList").innerHTML = repos.length ? repos.map(x => `
      <label class="repo-item">
        <input type="checkbox" value="${esc(x.full_name)}" ${all || selected.has(x.full_name) ? "checked" : ""} ${all ? "disabled" : ""}>
        <span class="repo-name">${esc(x.full_name)}</span>
        <span class="tags">
          ${x.private ? `<span class="tag">private</span>` : ""}
          ${x.fork ? `<span class="tag">fork</span>` : ""}
          ${x.archived ? `<span class="tag">archived</span>` : ""}
        </span>
      </label>`).join("") : `<div class="empty">Keine Repositories gefunden.</div>`;
  }

  async function saveRepos() {
    const id = state.repoConn;
    if (!id) return;
    const mode = $("#repoAll").checked ? "all" : "selected";
    const repos = $$("#repoList input:checked").map(x => x.value);
    if (mode === "selected" && repos.length === 0) return toast("Bitte mindestens ein Repository auswählen.", "error");
    try {
      await api(`api/connections/${id}/repos`, { method: "PUT", body: JSON.stringify({ mode, repos }) });
      $("#repoDialog").close();
      toast(mode === "all" ? "Alle Repositories ausgewählt" : `${repos.length} Repositories ausgewählt`, "success");
      await loadState();
    } catch (err) { toast(err.message, "error"); }
  }

  async function runNow(id) {
    try {
      await api("api/run", { method: "POST", body: JSON.stringify(id ? { connection_id: id } : {}) });
      toast("Backup gestartet", "success");
      await loadState();
    } catch (err) { toast(err.message, "error"); }
  }

  async function cancelRun() {
    try {
      await api("api/run/cancel", { method: "POST", body: "{}" });
      toast("Abbruch angefordert");
      await loadState();
    } catch (err) { toast(err.message, "error"); }
  }

  async function loadLogs() {
    const root = $("#logList");
    root.innerHTML = `<div class="empty">Lade Logs …</div>`;
    try {
      const list = await api("api/logs");
      if (!list.length) { root.innerHTML = `<div class="empty">Noch keine Logs vorhanden.</div>`; return; }
      root.innerHTML = list.map(l => `<div class="log-item" data-log="${esc(l.name)}"><span class="log-name">${esc(l.name)}</span><span class="muted">${esc(fmtDate(l.mtime))} · ${esc(fmtBytes(l.size))}</span></div>`).join("");
    } catch (err) { root.innerHTML = `<div class="empty">${esc(err.message)}</div>`; }
  }

  async function openLog(name) {
    $("#logTitle").textContent = name;
    $("#logContent").textContent = "Lade …";
    $("#logDialog").showModal();
    try {
      const r = await api(`api/logs/${encodeURIComponent(name)}`);
      $("#logContent").textContent = r.content || "(leer)";
    } catch (err) { $("#logContent").textContent = err.message; }
  }

  function fillSettings() {
    const d = state.data;
    if (!d) return;
    $("#setDataRoot").value = d.data_root || "";
    $("#setWeekday").value = d.schedule?.weekday ?? 0;
    $("#setHour").value = d.schedule?.hour ?? 3;
    $("#setMinute").value = d.schedule?.minute ?? 0;
    $("#setSchedule").checked = !!d.schedule?.enabled;
    $("#setWikis").checked = !!d.options?.include_wikis;
    $("#setPrune").checked = !!d.options?.prune_deleted;
    $("#setNotify").checked = !!d.options?.notify;
    $("#setConcurrency").value = d.options?.concurrency ?? 1;
    $("#setKeepLogs").value = d.options?.keep_log_days ?? 90;
  }

  async function saveSettings(e) {
    e.preventDefault();
    const body = {
      data_root: $("#setDataRoot").value.trim(),
      schedule: {
        enabled: $("#setSchedule").checked,
        weekday: Number($("#setWeekday").value),
        hour: Number($("#setHour").value),
        minute: Number($("#setMinute").value),
      },
      options: {
        include_wikis: $("#setWikis").checked,
        prune_deleted: $("#setPrune").checked,
        notify: $("#setNotify").checked,
        concurrency: Number($("#setConcurrency").value),
        keep_log_days: Number($("#setKeepLogs").value),
        git_retries: state.data?.options?.git_retries ?? 2,
      },
    };
    try {
      await api("api/settings", { method: "PUT", body: JSON.stringify(body) });
      toast("Einstellungen gespeichert", "success");
      await loadState();
    } catch (err) { toast(err.message, "error"); }
  }

  document.addEventListener("click", (e) => {
    const nav = e.target.closest("[data-page]");
    if (nav) { navigate(nav.dataset.page); return; }
    const run = e.target.closest("[data-run]");
    if (run) { runNow(run.dataset.run); return; }
    const repos = e.target.closest("[data-repos]");
    if (repos) { openRepos(repos.dataset.repos); return; }
    const edit = e.target.closest("[data-edit]");
    if (edit) { openConn(edit.dataset.edit); return; }
    const test = e.target.closest("[data-test]");
    if (test) { testConn(test.dataset.test); return; }
    const log = e.target.closest("[data-log]");
    if (log) { openLog(log.dataset.log); return; }
  });

  $("#runBtn").addEventListener("click", () => runNow());
  $("#cancelBtn").addEventListener("click", cancelRun);
  $("#addConnBtn").addEventListener("click", () => openConn());
  $("#connCancel").addEventListener("click", () => $("#connDialog").close());
  $("#connDelete").addEventListener("click", deleteConn);
  $("#connForm").addEventListener("submit", saveConn);
  $("#connKind").addEventListener("change", () => $("#ownerWrap").classList.toggle("hidden", $("#connKind").value !== "org"));
  $("#repoCancel").addEventListener("click", () => $("#repoDialog").close());
  $("#repoSave").addEventListener("click", saveRepos);
  $("#repoAll").addEventListener("change", renderRepoList);
  $("#repoSearch").addEventListener("input", renderRepoList);
  $("#refreshLogsBtn").addEventListener("click", loadLogs);
  $("#logClose").addEventListener("click", () => $("#logDialog").close());
  $("#settingsForm").addEventListener("submit", saveSettings);
  $("#dsmLink").addEventListener("click", () => { window.top.location.href = "/"; });

  init();
})();
