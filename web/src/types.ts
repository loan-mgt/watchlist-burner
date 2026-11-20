export interface Setup {
  lbUser: string;
  jellyfinUrl: string;
  jellyfinUser: string;
}

export interface Session {
  url: string;
  token: string;
  userId: string;
  userName: string;
  serverId: string;
}

export interface Film {
  slug: string;
  title: string;
  year: number;
  posterUrl?: string;
  letterboxdUrl: string;
  itemId?: string;
  inLibrary: boolean;
}

export interface Meta {
  total: number;
  matched: number;
  watched: number;
}

export type Frame =
  | { type: "phase"; phase: string }
  | { type: "film"; title: string; year?: number; index: number; total: number }
  | { type: "result"; films: Film[]; meta: Meta }
  | { type: "error"; code: string; message: string }
  | { type: "ping" };
