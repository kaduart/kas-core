"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { z } from "zod";
import { BrandMark, BrandName } from "@/components/shell/BrandMark";
import { cn } from "@/lib/cn";
import { contrast, isHex, mix } from "@/lib/tenant/color";
import { useTenant } from "@/lib/tenant/TenantProvider";
import type { BrandIcon } from "@/lib/tenant/types";

const schema = z.object({
  name1: z.string().trim().min(1, "Informe o nome do sistema.").max(14, "Use até 14 letras para caber no menu."),
  name2: z.string().max(14, "Use até 14 letras para caber no menu."),
  tagline: z.string().trim().max(40, "Use até 40 letras."),
  primary: z
    .string()
    .refine(isHex, "Use uma cor no formato #RRGGBB.")
    .refine((v) => !isHex(v) || contrast(mix(v, "#000000", 0.55), "#ffffff") >= 4.5, "Cor clara demais: o texto branco do menu ficaria ilegível."),
  accent: z.string().refine(isHex, "Use uma cor no formato #RRGGBB."),
  icon: z.enum(["x", "leaf", "heart", "tooth"]),
});
type BrandValues = z.infer<typeof schema>;

const PRIMARY = ["#0E8F4E", "#1F5FD1", "#6B3FD1", "#D1453F", "#0F7C8C", "#B7791F"];
const ACCENT = ["#C6F03C", "#7FE3FF", "#FFC94D", "#FF9FB7", "#9CF2C8", "#FFFFFF"];
const ICONS: { k: BrandIcon; l: string }[] = [
  { k: "x", l: "X da KAS" },
  { k: "leaf", l: "Folha" },
  { k: "heart", l: "Coração" },
  { k: "tooth", l: "Dente" },
];

const input = "min-h-12 w-full rounded-xl border border-[#d5ddd8] bg-white px-3.5 text-[15px] text-ink";

export function BrandForm() {
  const { tenant, previewBrand, saveBrand, resetBrand } = useTenant();
  const [saved, setSaved] = useState("");
  const form = useForm<BrandValues>({ resolver: zodResolver(schema), defaultValues: tenant.brand, mode: "onChange" });
  const { register, handleSubmit, setValue, reset, control, formState } = form;
  const values = useWatch({ control }) as BrandValues;

  // Ao trocar de empresa, o formulário passa a editar a marca dela.
  useEffect(() => {
    reset(tenant.brand);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tenant.id]);

  // Pré-visualização ao vivo: só aplica quando o valor é válido.
  useEffect(() => {
    const parsed = schema.safeParse(values);
    if (parsed.success) previewBrand(parsed.data);
  }, [values, previewBrand]);

  const onSubmit = (v: BrandValues) => {
    saveBrand(v);
    setSaved(`Marca salva para ${tenant.name}.`);
    setTimeout(() => setSaved(""), 2600);
  };

  const err = formState.errors;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="grid max-w-[1100px] gap-4 xl:grid-cols-[1fr_300px]">
      <section className="rounded-[22px] bg-white px-5 py-[18px] shadow-[0_1px_3px_rgba(15,31,23,.05)]">
        <h2 className="text-[21px] font-bold tracking-tight">Marca da empresa</h2>
        <p className="mb-4 mt-1 text-[13.5px] text-muted">
          Cada empresa vê o sistema com o próprio nome, cores e ícone. O layout continua o mesmo. As mudanças aparecem na hora.
        </p>
        <div className="grid gap-4 md:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block text-[13.5px] font-semibold">Nome do sistema (parte forte)</span>
            <input className={input} {...register("name1")} aria-invalid={!!err.name1} />
            {err.name1 ? <span className="mt-1 block text-[13px] text-[#a3402b]">{err.name1.message}</span> : null}
          </label>
          <label className="block">
            <span className="mb-1.5 block text-[13.5px] font-semibold">Nome do sistema (parte leve)</span>
            <input className={input} {...register("name2")} aria-invalid={!!err.name2} />
            {err.name2 ? <span className="mt-1 block text-[13px] text-[#a3402b]">{err.name2.message}</span> : null}
          </label>
          <label className="block md:col-span-2">
            <span className="mb-1.5 block text-[13.5px] font-semibold">Frase abaixo do nome</span>
            <input className={input} {...register("tagline")} aria-invalid={!!err.tagline} />
            {err.tagline ? <span className="mt-1 block text-[13px] text-[#a3402b]">{err.tagline.message}</span> : null}
          </label>

          <fieldset>
            <legend className="mb-1.5 text-[13.5px] font-semibold">Cor principal</legend>
            <div className="flex flex-wrap items-center gap-2.5">
              {PRIMARY.map((c) => (
                <button
                  type="button"
                  key={c}
                  aria-label={`Cor ${c}`}
                  aria-pressed={values.primary?.toLowerCase() === c.toLowerCase()}
                  onClick={() => setValue("primary", c, { shouldValidate: true, shouldDirty: true })}
                  className={cn("size-11 rounded-full border-[3px] border-white ring-1 ring-[#d5ddd8]", values.primary?.toLowerCase() === c.toLowerCase() && "ring-[3px] ring-ink")}
                  style={{ background: c }}
                />
              ))}
              <input type="color" aria-label="Outra cor principal" className="size-11 cursor-pointer bg-transparent" {...register("primary")} />
            </div>
            {err.primary ? <span className="mt-1 block text-[13px] text-[#a3402b]">{err.primary.message}</span> : null}
          </fieldset>

          <fieldset>
            <legend className="mb-1.5 text-[13.5px] font-semibold">Cor de destaque</legend>
            <div className="flex flex-wrap items-center gap-2.5">
              {ACCENT.map((c) => (
                <button
                  type="button"
                  key={c}
                  aria-label={`Cor ${c}`}
                  aria-pressed={values.accent?.toLowerCase() === c.toLowerCase()}
                  onClick={() => setValue("accent", c, { shouldValidate: true, shouldDirty: true })}
                  className={cn("size-11 rounded-full border-[3px] border-white ring-1 ring-[#d5ddd8]", values.accent?.toLowerCase() === c.toLowerCase() && "ring-[3px] ring-ink")}
                  style={{ background: c }}
                />
              ))}
              <input type="color" aria-label="Outra cor de destaque" className="size-11 cursor-pointer bg-transparent" {...register("accent")} />
            </div>
          </fieldset>

          <fieldset className="md:col-span-2">
            <legend className="mb-1.5 text-[13.5px] font-semibold">Ícone</legend>
            <div className="flex flex-wrap gap-2.5">
              {ICONS.map((i) => (
                <button
                  type="button"
                  key={i.k}
                  aria-label={i.l}
                  aria-pressed={values.icon === i.k}
                  onClick={() => setValue("icon", i.k, { shouldDirty: true })}
                  className={cn("grid size-16 place-items-center rounded-2xl border-2 bg-brand-deep", values.icon === i.k ? "border-accent" : "border-[#e1e7e4]")}
                >
                  <BrandMark icon={i.k} size={38} />
                </button>
              ))}
            </div>
            <p className="mt-1.5 text-[13px] text-muted">Na próxima etapa, a empresa também poderá enviar o próprio logo.</p>
          </fieldset>
        </div>

        <div className="mt-5 flex flex-wrap gap-2.5">
          <button type="submit" className="min-h-[46px] rounded-[14px] bg-brand px-[18px] font-semibold text-white disabled:opacity-60" disabled={!formState.isValid}>
            Salvar marca
          </button>
          <button
            type="button"
            onClick={() => {
              reset(resetBrand());
              setSaved("Marca padrão KAS aplicada.");
              setTimeout(() => setSaved(""), 2600);
            }}
            className="min-h-[46px] rounded-[14px] border border-[#d5ddd8] bg-white px-[18px] font-semibold"
          >
            Voltar ao padrão KAS
          </button>
          <p role="status" className="self-center text-sm font-medium text-brand-ink">
            {saved}
          </p>
        </div>
      </section>

      <aside aria-label="Prévia do menu" className="rounded-[22px] py-5 pl-3 text-white" style={{ background: "linear-gradient(180deg, var(--brand-deep), var(--brand-dark))" }}>
        <div className="flex items-center gap-2.5 px-3 pb-4">
          <BrandMark icon={values.icon ?? "x"} size={38} />
          <div className="min-w-0">
            <BrandName name1={values.name1 ?? ""} name2={values.name2 ?? ""} className="block truncate text-[22px] leading-none" />
            <small className="mt-1 block truncate text-[10.5px] font-semibold opacity-90">{values.tagline}</small>
          </div>
        </div>
        <div className="nav-active flex min-h-[42px] items-center rounded-l-[28px] pl-4 font-bold">Dashboard</div>
        <div className="flex min-h-[42px] items-center pl-4">Attribution</div>
        <div className="flex min-h-[42px] items-center pl-4">CRM</div>
      </aside>
    </form>
  );
}
