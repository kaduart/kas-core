"use server";

import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";
import { CoreError, coreConfigFromEnv, login, logout } from "@/lib/kas/core-client";
import { loginErrorMessage, safeNext } from "@/lib/kas/login-errors";
import { SESSION_COOKIE } from "@/lib/session";
import { TENANT_COOKIE } from "@/lib/tenant/presets";

export interface LoginState {
  error?: string;
  email?: string;
}

async function isHttps(): Promise<boolean> {
  const h = await headers();
  return (h.get("x-forwarded-proto") ?? "").split(",")[0].trim() === "https";
}

export async function loginAction(_prev: LoginState, form: FormData): Promise<LoginState> {
  const cfg = coreConfigFromEnv();
  if (!cfg) redirect("/dashboard");

  const email = String(form.get("email") ?? "").trim();
  const password = String(form.get("password") ?? "");
  if (!email || !password) return { error: "Preencha e-mail e senha.", email };

  let session;
  try {
    session = await login(cfg, email, password);
  } catch (e) {
    if (!(e instanceof CoreError)) console.error("login_failed", e);
    return { error: loginErrorMessage(e), email };
  }

  const jar = await cookies();
  const maxAge = Math.max(60, Math.floor((Date.parse(session.expires_at) - Date.now()) / 1000));
  jar.set(SESSION_COOKIE, session.session_token, { httpOnly: true, sameSite: "lax", secure: await isHttps(), path: "/", maxAge });
  // Escolha de empresa anterior só vale se a pessoa ainda tem acesso a ela.
  const prevTenant = jar.get(TENANT_COOKIE)?.value;
  if (prevTenant && !session.memberships.some((m) => m.tenant_id === prevTenant)) jar.delete(TENANT_COOKIE);
  redirect(safeNext(form.get("next")));
}

export async function logoutAction(): Promise<void> {
  const cfg = coreConfigFromEnv();
  const jar = await cookies();
  const token = jar.get(SESSION_COOKIE)?.value;
  if (cfg && token) {
    try {
      await logout(cfg, token);
    } catch (e) {
      // A sessão já pode ter expirado; o cookie sai de qualquer jeito.
      if (!(e instanceof CoreError && e.status === 401)) console.error("logout_failed", e);
    }
  }
  jar.delete(SESSION_COOKIE);
  redirect("/login");
}
