import type { Metadata } from "next";
import { ModuleGate } from "@/components/modules/ModuleGate";
import { AiView } from "@/components/modules/AiView";

export const metadata: Metadata = { title: "Amanda IA · KAS Core" };

export default function Page() {
  return (
    <ModuleGate module="ai"><AiView /></ModuleGate>
  );
}
