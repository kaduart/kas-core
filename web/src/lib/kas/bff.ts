import { NextResponse } from "next/server";
import { CoreError, coreConfigFromEnv, type CoreConfig } from "./core-client";
import { getSession, type Session } from "@/lib/session";

/** Guarda das rotas /api/kas: só modo real e com sessão. A empresa vem sempre da sessão, nunca da URL. */
export async function bffContext(): Promise<{ cfg: CoreConfig; session: Session } | NextResponse> {
  const cfg = coreConfigFromEnv();
  if (!cfg) return NextResponse.json({ error: { code: "demo_mode" } }, { status: 404 });
  let session: Session | null;
  try {
    session = await getSession();
  } catch (e) {
    return bffError(e);
  }
  if (!session) return NextResponse.json({ error: { code: "session_invalid" } }, { status: 401 });
  return { cfg, session };
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
