/** Validação das entradas do BFF da Agenda. Pura: usada nas rotas /api/kas/agenda e nos testes. */
import { z } from "zod";
import { REF_PATTERN } from "./contract";

const ref = z.string().regex(REF_PATTERN);
const localDate = z.string().regex(/^\d{4}-\d{2}-\d{2}$/);
const instant = z.string().datetime({ offset: true });

export const bookingsQuery = z.object({ recurso: ref.optional(), de: instant, ate: instant });
export const slotsQuery = z
  .object({ recurso: ref, de: localDate, ate: localDate, servico: ref.optional(), duracao: z.coerce.number().int().min(5).max(720).optional() })
  .refine((q) => !(q.servico && q.duracao), "servico ou duracao");

export const createBody = z
  .object({
    chave: ref,
    recurso: ref,
    cliente: ref,
    servico: ref.optional(),
    inicio: instant,
    fim: instant,
    status: z.enum(["tentative", "scheduled", "confirmed"]).optional(),
  })
  .strict();

export const cancelBody = z.object({ versao: z.number().int().min(1) }).strict();
export const rescheduleBody = z.object({ versao: z.number().int().min(1), inicio: instant, fim: instant }).strict();
export const bookingId = ref;

/** Lê os parâmetros de busca como objeto simples (último valor vence, igual ao Next). */
export const params = (u: URL) => Object.fromEntries(u.searchParams.entries());
