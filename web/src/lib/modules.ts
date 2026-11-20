import { Bot, Boxes, CalendarDays, ClipboardList, Package, Wallet, ChartNoAxesColumnIncreasing, FileText, House, MessageSquareText, Settings, UsersRound, Workflow, type LucideIcon } from "lucide-react";
import type { ModuleKey } from "./tenant/types";

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  module?: ModuleKey;
  badge?: number;
}

export interface ModuleInfo {
  key: ModuleKey;
  slug: string;
  label: string;
  description: string;
  icon: LucideIcon;
}

export const MODULES: ModuleInfo[] = [
  { key: "attribution", slug: "attribution", label: "Attribution", description: "Campanhas, conversões e receita", icon: ChartNoAxesColumnIncreasing },
  { key: "crm", slug: "crm", label: "CRM", description: "Leads, funil e relacionamento", icon: UsersRound },
  { key: "omnichannel", slug: "omnichannel", label: "Omnichannel", description: "WhatsApp e canais integrados", icon: MessageSquareText },
  { key: "process", slug: "process-studio", label: "Process Studio", description: "Automação de processos e SLAs", icon: Workflow },
  { key: "ai", slug: "amanda-ia", label: "Amanda IA", description: "Atendimento e classificação", icon: Bot },
  { key: "reports", slug: "relatorios", label: "Relatórios", description: "Dashboards e exportações", icon: FileText },
];

export const NAV_TOP: NavItem[] = [
  { href: "/dashboard", label: "Dashboard", icon: House },
  ...MODULES.map((m) => ({ href: `/${m.slug}`, label: m.label, icon: m.icon, module: m.key, badge: m.key === "omnichannel" ? 3 : undefined })),
];

export const NAV_BOTTOM: NavItem[] = [
  { href: "/modulos", label: "Módulos", icon: Boxes },
  { href: "/configuracoes", label: "Configurações", icon: Settings },
];

/** Módulos do ERP de clínicas ainda em construção: aparecem no catálogo como "Em breve". */
export const PLANNED: { label: string; description: string; icon: LucideIcon }[] = [
  { label: "Agenda clínica", description: "Agenda por profissional, confirmação e lista de espera", icon: CalendarDays },
  { label: "Financeiro", description: "Recebimentos, pacotes, cobranças e despesas", icon: Wallet },
  { label: "Prontuário", description: "Evolução, anexos e histórico do paciente", icon: ClipboardList },
  { label: "Estoque", description: "Materiais e insumos com alerta de reposição", icon: Package },
];
