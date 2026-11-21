import { NextResponse, type NextRequest } from "next/server";
import { badRequest, bffContext, bffError, forbiddenOrigin, sameOrigin } from "@/lib/kas/bff";
import { bookingsQuery, createBody, params } from "@/lib/kas/agenda-input";
import { createAgendaBooking, listAgendaBookings } from "@/lib/kas/core-client";

export const dynamic = "force-dynamic";

/** Agendamentos da empresa da sessão que tocam [de, ate). */
export async function GET(req: NextRequest) {
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const q = bookingsQuery.safeParse(params(req.nextUrl));
  if (!q.success) return badRequest();
  try {
    const bookings = await listAgendaBookings(ctx.cfg, ctx.session.tenantId, ctx.token, { resourceId: q.data.recurso, from: q.data.de, to: q.data.ate });
    return NextResponse.json({ bookings }, { headers: { "Cache-Control": "no-store" } });
  } catch (e) {
    return bffError(e);
  }
}

/** Cria um agendamento. A chave vem do navegador (uma por tentativa), então reenviar não duplica. */
export async function POST(req: NextRequest) {
  if (!sameOrigin(req)) return forbiddenOrigin();
  const ctx = await bffContext();
  if (ctx instanceof NextResponse) return ctx;
  const b = createBody.safeParse(await req.json().catch(() => null));
  if (!b.success) return badRequest();
  try {
    const out = await createAgendaBooking(ctx.cfg, ctx.session.tenantId, ctx.token, {
      idempotencyKey: b.data.chave,
      resourceId: b.data.recurso,
      clientRef: b.data.cliente,
      serviceId: b.data.servico,
      startsAt: b.data.inicio,
      endsAt: b.data.fim,
      status: b.data.status,
    });
    return NextResponse.json(out, { status: out.replayed ? 200 : 201 });
  } catch (e) {
    return bffError(e);
  }
}
