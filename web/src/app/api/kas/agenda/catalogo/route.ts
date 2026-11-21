import { NextResponse } from "next/server";
import { bffContext, bffError } from "@/lib/kas/bff";
import { getAgendaCatalog } from "@/lib/kas/core-client";

export const dynamic = "force-dynamic";

/** Profissionais/salas, serviços, fuso e se a agenda é só leitura (GET /internal/v1/scheduling/catalog). */
export async function GET() {
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  try {
    return NextResponse.json(await getAgendaCatalog(ctx.cfg, ctx.session.tenantId, ctx.token), { headers: { "Cache-Control": "no-store" } });
  } catch (e) {
    return bffError(e);
  }
}
