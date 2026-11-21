import { NextResponse, type NextRequest } from "next/server";
import { badRequest, bffContext, bffError } from "@/lib/kas/bff";
import { params, slotsQuery } from "@/lib/kas/agenda-input";
import { getAgendaSlots } from "@/lib/kas/core-client";

export const dynamic = "force-dynamic";

/** Horários livres de um recurso entre duas datas locais (GET /internal/v1/scheduling/availability). */
export async function GET(req: NextRequest) {
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const q = slotsQuery.safeParse(params(req.nextUrl));
  if (!q.success) return badRequest();
  try {
    const slots = await getAgendaSlots(ctx.cfg, ctx.session.tenantId, ctx.token, {
      resourceId: q.data.recurso,
      from: q.data.de,
      to: q.data.ate,
      serviceId: q.data.servico,
      durationMin: q.data.duracao,
    });
    return NextResponse.json({ slots }, { headers: { "Cache-Control": "no-store" } });
  } catch (e) {
    return bffError(e);
  }
}
