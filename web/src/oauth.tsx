import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { useT } from "./i18n";
import { Icon } from "./icons";
import type { Config, OAuthStart, Provider } from "./types";
import { OAUTH_PROVIDER_TYPE } from "./types";
import { CopyButton, ErrorNote, useConfirm } from "./ui";
import type { Key } from "./dict";
import { clone, copyText } from "./util";

export const OAUTH_LABEL: Record<string, string> = {
  codex: "ChatGPT (Codex)",
  claude: "Claude",
  copilot: "GitHub Copilot",
  "gemini-cli": "Google (Gemini CLI)",
};

export function OAuthWarning() {
  const t = useT();
  return (
    <div className="alert warn" role="note">
      <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
      <span>{t("oauth.warning")}</span>
    </div>
  );
}

const RISK: Record<string, Key> = {
  claude: "oauth.riskClaude",
  codex: "oauth.riskCodex",
  copilot: "oauth.riskCopilot",
  "gemini-cli": "oauth.riskGemini",
};

/**
 * Asks the admin to acknowledge the terms-of-service risk before logging in
 * with a personal subscription. Resolves true when they accept.
 */
export function useSubscriptionConsent(): (oauthType: string) => Promise<boolean> {
  const t = useT();
  const confirm = useConfirm();
  return (oauthType) =>
    confirm({
      title: t("oauth.consentTitle", { x: OAUTH_LABEL[oauthType] || oauthType }),
      danger: true,
      confirm: t("oauth.consentOk"),
      body: (
        <div className="stack" style={{ marginTop: 0 }}>
          <p>{t(RISK[oauthType] || "oauth.riskGeneric")}</p>
          <ul style={{ margin: 0, paddingLeft: 18 }}>
            <li>{t("oauth.consent1")}</li>
            <li>{t("oauth.consent2")}</li>
            <li>{t("oauth.consent3")}</li>
          </ul>
        </div>
      ),
    });
}

/** Adds accountId to a provider of the matching type (creating one if needed). */
export function attachAccount(cfg: Config, oauthType: string, accountId: string, providerId?: string): { next: Config; providerId: string; created: boolean } {
  const next = clone(cfg);
  const ptype = OAUTH_PROVIDER_TYPE[oauthType] || oauthType;
  let p = providerId ? next.providers.find((x) => x.id === providerId) : next.providers.find((x) => x.type === ptype);
  let created = false;
  if (!p) {
    let id = ptype;
    for (let i = 2; next.providers.some((x) => x.id === id) || next.combos.some((c) => c.name === id); i++) id = `${ptype}-${i}`;
    p = { id, type: ptype, base_url: "", keys: [], oauth: [], models: [], model_map: {}, headers: {}, proxy: "", priority: 0, disabled: false, first_byte: 0, idle: 0 } as Provider;
    next.providers.push(p);
    created = true;
  }
  if (!p.oauth.includes(accountId)) p.oauth = [...p.oauth, accountId];
  return { next, providerId: p.id, created };
}

export function OAuthFlow({ oauthType, onDone, onCancel }: { oauthType: string; onDone: (accountId: string) => void; onCancel: () => void }) {
  const t = useT();
  const [accountId, setAccountId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [flow, setFlow] = useState<OAuthStart | null>(null);
  const [code, setCode] = useState("");
  const [err, setErr] = useState<string>();
  const [busy, setBusy] = useState(false);
  const doneRef = useRef(false);

  const finish = (id: string) => {
    if (doneRef.current) return;
    doneRef.current = true;
    onDone(id);
  };

  useEffect(() => {
    if (!flow) return;
    let alive = true;
    let timer: number | undefined;
    const ctrl = new AbortController();
    const poll = async () => {
      try {
        const st = await api.oauthStatus(flow.flow_id, ctrl.signal);
        if (!alive) return;
        if (st.state === "done" && st.account_id) return finish(st.account_id);
        if (st.state === "error") {
          setErr(st.error || "OAuth error");
          return;
        }
      } catch (e) {
        if (!alive || (e as Error).name === "AbortError") return;
        setErr((e as Error).message);
        return;
      }
      timer = window.setTimeout(poll, 2000);
    };
    timer = window.setTimeout(poll, 2000);
    return () => {
      alive = false;
      window.clearTimeout(timer);
      ctrl.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flow]);

  const start = async () => {
    setBusy(true);
    setErr(undefined);
    doneRef.current = false;
    try {
      const f = await api.oauthStart({ type: oauthType, account_id: accountId.trim() || undefined, project_id: oauthType === "gemini-cli" ? projectId.trim() || undefined : undefined });
      setFlow(f);
      if (f.auth_url && f.mode !== "device") window.open(f.auth_url, "_blank", "noopener");
      if (f.mode === "device" && f.user_code) copyText(f.user_code);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const complete = async () => {
    if (!flow || !code.trim()) return;
    setBusy(true);
    setErr(undefined);
    try {
      const info = await api.oauthComplete(flow.flow_id, code.trim());
      finish(info.account_id);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const label = OAUTH_LABEL[oauthType] || oauthType;

  return (
    <div className="card card-pad stack" style={{ background: "var(--surface-2)" }}>
      <div className="row">
        <Icon name="key" />
        <h3 className="grow">{t("oauth.loginWith", { x: label })}</h3>
        <button type="button" className="btn ghost sm" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
      <OAuthWarning />
      {err && <ErrorNote error={err} />}
      {!flow ? (
        <>
          <div className="grid-2">
            <div className="field">
              <label htmlFor="oa-acc">{t("oauth.accountId")}</label>
              <input id="oa-acc" className="input mono" value={accountId} onChange={(e) => setAccountId(e.target.value)} placeholder={t("oauth.auto")} aria-describedby="oa-acc-h" />
              <span className="hint" id="oa-acc-h">
                {t("oauth.accountIdHint")}
              </span>
            </div>
            {oauthType === "gemini-cli" && (
              <div className="field">
                <label htmlFor="oa-proj">Google Cloud project ID</label>
                <input id="oa-proj" className="input mono" value={projectId} onChange={(e) => setProjectId(e.target.value)} placeholder={t("oauth.auto")} />
              </div>
            )}
          </div>
          <div>
            <button type="button" className="btn primary" onClick={start} disabled={busy}>
              <Icon name="lock" size={13} /> {busy ? t("common.loading") : t("oauth.start")}
            </button>
          </div>
        </>
      ) : (
        <>
          {flow.mode === "device" ? (
            <div className="stack">
              <p className="small">{t("oauth.deviceStep")}</p>
              <div className="row wrap">
                <span className="mono" style={{ fontSize: 26, fontWeight: 700, letterSpacing: "0.12em" }} aria-label={t("oauth.userCode")}>
                  {flow.user_code}
                </span>
                <CopyButton text={flow.user_code || ""} />
              </div>
              {flow.verification_uri && (
                <a className="btn" href={flow.verification_uri} target="_blank" rel="noopener noreferrer">
                  <Icon name="globe" size={13} /> {flow.verification_uri}
                </a>
              )}
            </div>
          ) : (
            <div className="stack">
              <p className="small">{flow.mode === "redirect" ? t("oauth.redirectStep") : t("oauth.pasteStep")}</p>
              {flow.auth_url && (
                <div className="row wrap">
                  <a className="btn" href={flow.auth_url} target="_blank" rel="noopener noreferrer">
                    <Icon name="globe" size={13} /> {t("oauth.openAgain")}
                  </a>
                  <CopyButton text={flow.auth_url} label={t("oauth.copyLink")} />
                </div>
              )}
              <div className="field">
                <label htmlFor="oa-code">{flow.mode === "redirect" ? t("oauth.fallbackLabel") : t("oauth.codeLabel")}</label>
                <div className="row">
                  <input
                    id="oa-code"
                    className="input mono"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="http://localhost:…/callback?code=… / code#state"
                    autoComplete="off"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        complete();
                      }
                    }}
                  />
                  <button type="button" className="btn primary" onClick={complete} disabled={busy || !code.trim()}>
                    {t("oauth.submitCode")}
                  </button>
                </div>
                {flow.mode === "redirect" && <span className="hint">{t("oauth.fallbackHint")}</span>}
              </div>
            </div>
          )}
          {!err && (
            <div className="row small muted" role="status">
              <span className="dot warn" /> {t("oauth.waiting")}
            </div>
          )}
          {err && (
            <div>
              <button type="button" className="btn sm" onClick={() => { setFlow(null); setCode(""); setErr(undefined); }}>
                <Icon name="refresh" size={13} /> {t("common.retry")}
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
