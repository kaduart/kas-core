/**
 * Dados fictícios das telas de módulo. Mesma regra do painel: na integração,
 * vêm do KAS Core por tenant; o front não calcula nada de negócio.
 */
export interface CampaignRow {
  channel: string;
  color: string;
  campaign: string;
  leads: number;
  scheduled: number;
  revenue: number;
  spend: number;
}

/**
 * Escola Exemplo: a receita por canal fecha com o painel (Receita por Campanha, 30 dias = R$ 25.676),
 * leads somam 273 e visitas 115. Ver dashboardFixture.
 */
export const ATTRIBUTION_SCHOOL: CampaignRow[] = [
  { channel: "Meta Ads", color: "#F0216F", campaign: "Inglês para adultos, vídeo", leads: 70, scheduled: 29, revenue: 6820, spend: 1640 },
  { channel: "Meta Ads", color: "#F0216F", campaign: "Turmas infantis, carrossel", leads: 38, scheduled: 16, revenue: 3596, spend: 910 },
  { channel: "Google Ads", color: "#0E9F55", campaign: "Curso de inglês perto de mim", leads: 62, scheduled: 26, revenue: 5210, spend: 1380 },
  { channel: "Google Ads", color: "#0E9F55", campaign: "Espanhol intensivo", leads: 31, scheduled: 13, revenue: 2658, spend: 720 },
  { channel: "Indicação", color: "#2468F2", campaign: "Aluno indica aluno", leads: 40, scheduled: 17, revenue: 4144, spend: 0 },
  { channel: "Orgânico", color: "#FF8A1F", campaign: "Instagram e Google Meu Negócio", leads: 22, scheduled: 9, revenue: 2422, spend: 0 },
  { channel: "Outros", color: "#C9CED1", campaign: "Feiras e eventos", leads: 10, scheduled: 5, revenue: 826, spend: 0 },
];

export const ATTRIBUTION: CampaignRow[] = [
  { channel: "Google Ads", color: "#0E9F55", campaign: "Avaliação TEA, pesquisa", leads: 142, scheduled: 74, revenue: 14280, spend: 3120 },
  { channel: "Meta Ads", color: "#F0216F", campaign: "Fala infantil, vídeo", leads: 168, scheduled: 81, revenue: 12960, spend: 2840 },
  { channel: "Google Ads", color: "#0E9F55", campaign: "Fonoaudiologia perto de mim", leads: 61, scheduled: 33, revenue: 8702, spend: 1490 },
  { channel: "Meta Ads", color: "#F0216F", campaign: "Remarketing, site", leads: 39, scheduled: 18, revenue: 4550, spend: 760 },
  { channel: "TikTok Ads", color: "#FF8A1F", campaign: "Dicas para pais", leads: 44, scheduled: 15, revenue: 6019, spend: 1210 },
  { channel: "Orgânico", color: "#2468F2", campaign: "Instagram e Google Meu Negócio", leads: 28, scheduled: 15, revenue: 4921, spend: 0 },
];

export type StageKey = "novo" | "qualificado" | "agendado" | "compareceu" | "convertido";
export const STAGES: { key: StageKey; label: string }[] = [
  { key: "novo", label: "Novos" },
  { key: "qualificado", label: "Qualificados" },
  { key: "agendado", label: "Agendados" },
  { key: "compareceu", label: "Compareceram" },
  { key: "convertido", label: "Convertidos" },
];

export interface Lead {
  id: string;
  name: string;
  interest: string;
  source: string;
  stage: StageKey;
  since: string;
  value: number;
}

export const LEADS: Lead[] = [
  { id: "L-7321", name: "Juliana Martins", interest: "Avaliação de fala, filho de 3 anos", source: "Meta Ads", stage: "novo", since: "há 28 min", value: 220 },
  { id: "L-7319", name: "Rodrigo Sales", interest: "Avaliação TEA", source: "Google Ads", stage: "novo", since: "há 2 h", value: 220 },
  { id: "L-7310", name: "Aline Couto", interest: "Psicologia para adolescente", source: "Orgânico", stage: "novo", since: "há 3 h", value: 220 },
  { id: "L-7302", name: "Patrícia Lima", interest: "Gagueira, 7 anos", source: "Google Ads", stage: "qualificado", since: "ontem", value: 720 },
  { id: "L-7298", name: "Marcos Prado", interest: "Terapia ocupacional", source: "Meta Ads", stage: "qualificado", since: "ontem", value: 720 },
  { id: "L-7290", name: "Camila Rocha", interest: "Avaliação fono", source: "TikTok Ads", stage: "agendado", since: "seg", value: 220 },
  { id: "L-7285", name: "Tiago Ferreira", interest: "Pacote fono 1x/semana", source: "Google Ads", stage: "agendado", since: "seg", value: 720 },
  { id: "L-7270", name: "Luciana Andrade", interest: "Sessão avulsa psicologia", source: "Orgânico", stage: "compareceu", since: "sex", value: 220 },
  { id: "L-7262", name: "Sandra Alves", interest: "Pacote T.O.", source: "Meta Ads", stage: "convertido", since: "qui", value: 720 },
  { id: "L-7251", name: "Fernanda Teixeira", interest: "Pacote psicologia", source: "Google Ads", stage: "convertido", since: "qua", value: 720 },
];

export interface Conversation {
  id: string;
  name: string;
  channel: "WhatsApp" | "Instagram" | "Site";
  waiting?: string;
  unread: number;
  messages: { from: "client" | "team" | "amanda"; text: string; at: string }[];
}

export const CONVERSATIONS: Conversation[] = [
  {
    id: "c1",
    name: "Juliana Martins",
    channel: "WhatsApp",
    waiting: "3h 12min",
    unread: 2,
    messages: [
      { from: "client", text: "Oi! Meu filho tem 3 anos e fala poucas palavras. Vocês atendem?", at: "16:14" },
      { from: "amanda", text: "Oi, Juliana! Atendemos sim. Posso te passar como funciona a avaliação?", at: "16:14" },
      { from: "client", text: "Pode sim. E qual o valor?", at: "16:20" },
    ],
  },
  {
    id: "c2",
    name: "Rodrigo Sales",
    channel: "Instagram",
    waiting: "2h 40min",
    unread: 1,
    messages: [{ from: "client", text: "Como funciona a avaliação de TEA? Precisa de encaminhamento?", at: "16:46" }],
  },
  {
    id: "c3",
    name: "Aline Couto",
    channel: "Site",
    waiting: "2h 05min",
    unread: 1,
    messages: [{ from: "client", text: "Vocês têm psicóloga para adolescente? Ela tem 15 anos.", at: "17:21" }],
  },
  {
    id: "c4",
    name: "Mariana Lopes",
    channel: "WhatsApp",
    unread: 0,
    messages: [
      { from: "client", text: "Consigo remarcar a avaliação da minha filha para quinta?", at: "18:50" },
      { from: "team", text: "Consegue sim! Quinta de manhã fica bom?", at: "18:58" },
    ],
  },
];

export interface ProcessRow {
  id: string;
  name: string;
  steps: number;
  running: number;
  dueSoon: number;
  late: number;
  active: boolean;
}

export const PROCESSES: ProcessRow[] = [
  { id: "p1", name: "Renovação de pacote", steps: 5, running: 12, dueSoon: 3, late: 0, active: true },
  { id: "p2", name: "Retenção e cancelamento de contrato", steps: 7, running: 4, dueSoon: 1, late: 1, active: true },
  { id: "p3", name: "Primeiro atendimento de lead", steps: 4, running: 9, dueSoon: 2, late: 0, active: true },
  { id: "p4", name: "Cobrança de pagamento em aberto", steps: 3, running: 0, dueSoon: 0, late: 0, active: false },
];

export const AI_CLASSES = [
  { label: "Quer agendar avaliação", n: 118 },
  { label: "Dúvida sobre valores", n: 74 },
  { label: "Remarcação", n: 41 },
  { label: "Convênio ou reembolso", n: 23 },
  { label: "Outro assunto", n: 12 },
];

export const AI_RECENT = [
  { who: "Juliana Martins", said: "Qual o valor da avaliação?", cls: "Dúvida sobre valores", action: "Explicou o valor e ofereceu horários", handoff: false },
  { who: "Rodrigo Sales", said: "Precisa de encaminhamento para TEA?", cls: "Quer agendar avaliação", action: "Encaminhou para a recepção", handoff: true },
  { who: "Mariana Lopes", said: "Consigo remarcar para quinta?", cls: "Remarcação", action: "Ofereceu quinta de manhã", handoff: false },
  { who: "Carlos Dias", said: "Vocês atendem pelo plano X?", cls: "Convênio ou reembolso", action: "Explicou o reembolso", handoff: false },
];

export const REPORTS = [
  { id: "r1", name: "Receita por campanha", desc: "Receita atribuída a cada canal e campanha no período.", updated: "hoje, 18:00" },
  { id: "r2", name: "Funil comercial", desc: "Leads, qualificados, agendados, comparecimentos e conversões.", updated: "hoje, 18:00" },
  { id: "r3", name: "Pacotes a renovar", desc: "Pacotes que terminam nos próximos 14 dias.", updated: "hoje, 07:30" },
  { id: "r4", name: "Tempo de primeira resposta", desc: "Quanto cada contato esperou pela primeira resposta.", updated: "ontem, 23:00" },
  { id: "r5", name: "Faltas e remarcações", desc: "Faltas, remarcações e horários recuperados.", updated: "ontem, 23:00" },
];

export const REPORTS_SCHOOL = [
  { id: "r1", name: "Receita por campanha", desc: "Mensalidades recebidas atribuídas a cada canal e campanha.", updated: "hoje, 07:05" },
  { id: "r2", name: "Funil de matrículas", desc: "Leads, visitas, aulas teste e matrículas no período.", updated: "hoje, 07:05" },
  { id: "r3", name: "Conciliação de mensalidades", desc: "Linhas do CSV importadas, ignoradas por duplicidade e sem origem.", updated: "hoje, 07:00" },
  { id: "r4", name: "Custo por matrícula", desc: "Investimento em anúncios dividido pelas matrículas de cada canal.", updated: "hoje, 07:05" },
];
