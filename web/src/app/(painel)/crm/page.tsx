import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { CrmView } from "@/components/modules/CrmView";

export const metadata: Metadata = { title: "CRM · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="crm"><CrmView /></ModuleGate>
  );
}
