import type { Metadata } from "next";
import { CatalogView } from "@/components/modules/CatalogView";

export const metadata: Metadata = { title: "Módulos · KAS Core" };

export default function Page() {
  return <CatalogView />;
}
