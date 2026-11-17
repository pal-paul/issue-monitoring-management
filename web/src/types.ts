export interface Issue {
  id: string;
  incidentId: string;
  project: string;
  policyName: string;
  summary: string;
  resourceName: string;
  resourceType: string;
  severity: 'critical' | 'warning' | 'info';
  status: 'open' | 'acknowledged' | 'resolved';
  startedAt: string;
  endedAt: string | null;
  receivedAt: string;
  updatedAt: string;
  acknowledgedAt: string | null;
  monitoringUrl: string;
  payload?: { incident?: { documentation?: { content?: string }; resource?: { labels?: Record<string, string> } } };
}

export interface IssueEvent { id: number; kind: string; eventAt: string; receivedAt: string; messageId: string; actor: string; body?: string; bodyFormat?: 'plain' | 'markdown'; payload?: unknown }
export interface Detail { issue: Issue; events: IssueEvent[] }
export interface IssueList { issues: Issue[]; total: number; page: number; limit: number }
export interface Overview {
  total: number; open: number; critical: number; acknowledged: number; resolved: number;
  lastReceived: string | null; projects: string[];
  hours: { hour: string; critical: number; warning: number; info: number }[];
}
export interface Connection {
  source: string; transport: string; database: string;
  subscription: string; enabled: boolean; demo: boolean;
  worker: { state: string; error?: string; lastPoll?: string; lastMessage?: string };
}

export interface User { id: string; username: string; name: string; role: 'admin' | 'operator' | 'viewer'; enabled: boolean; permissions: string[] }
export interface Session { databaseConfigured: boolean; setupRequired: boolean; user: User | null; csrf: string; local: boolean }
export interface Settings { workspaceId: string; subscription: string; enabled: boolean }
