import type { Metadata } from "next";
import { BrandForm } from "@/components/settings/BrandForm";

export const metadata: Metadata = { title: "Configurações · KAS Core" };

export default function ConfiguracoesPage() {
  return <BrandForm />;
}
