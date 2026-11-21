import { NextResponse } from "next/server";
import { CoreError, coreConfigFromEnv, type CoreConfig } from "./core-client";
import { getSession, sessionToken, type Session } from "@/lib/session";

/** Guarda das rotas /api/kas: só modo real e com sessão. A empresa vem sempre da sessão, nunca da URL. */
export async function bffContext(): Promise<{ cfg: CoreConfig; session: Session; token: string } | NextResponse> {
  const cfg = coreConfigFromEnv();
  if (!cfg) return NextResponse.json({ error: { code: "demo_mode" } }, { status: 404 });
  let session: Session | null;
  try {
    session = await getSession();
  } catch (e) {
    return bffError(e);
  }
  const token = await sessionToken();
  if (!session || !token) return NextResponse.json({ error: { code: "session_invalid" } }, { status: 401 });
  return { cfg, session, token };
}

export function bffError(e: unknown): NextResponse {
  if (e instanceof CoreError) {
    // Repassa o código do Go; 5xx vira 502 para o navegador saber que foi o servidor de dados.
    const status = e.status >= 500 ? 502 : e.status;
    return NextResponse.json({ error: { code: e.code } }, { status });
  }
  console.error("bff_failed", e);
  return NextResponse.json({ error: { code: "internal" } }, { status: 500 });
}

/**
 * Escritas do navegador (POST com o cookie de sessão) só valem vindas do próprio painel:
 * Origin precisa bater com o Host. Barra o envio de formulário por outro site (CSRF).
 */
export function sameOrigin(req: Request): boolean {
  const origin = req.headers.get("origin");
  const host = req.headers.get("x-forwarded-host") ?? req.headers.get("host");
  if (!origin || !host) return false;
  try {
    return new URL(origin).host === host;
  } catch {
    return false;
  }
}

export const forbiddenOrigin = () => NextResponse.json({ error: { code: "origin_forbidden" } }, { status: 403 });
export const badRequest = () => NextResponse.json({ error: { code: "invalid_request" } }, { status: 400 });
