export namespace core {
	
	export class ProgressRow {
	    narration?: string;
	    toolName?: string;
	    inputSummary?: string;
	    finished?: boolean;
	    isError?: boolean;
	    elapsedMs?: number;
	
	    static createFrom(source: any = {}) {
	        return new ProgressRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.narration = source["narration"];
	        this.toolName = source["toolName"];
	        this.inputSummary = source["inputSummary"];
	        this.finished = source["finished"];
	        this.isError = source["isError"];
	        this.elapsedMs = source["elapsedMs"];
	    }
	}

}

export namespace main {
	
	export class AgentInfo {
	    name: string;
	    displayName: string;
	    team?: string;
	    tagline?: string;
	    accentColor?: string;
	    avatar?: string;
	    state: string;
	    open: boolean;
	    active: boolean;
	    mark: string;
	    label: string;
	    unread: number;
	    working: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AgentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.displayName = source["displayName"];
	        this.team = source["team"];
	        this.tagline = source["tagline"];
	        this.accentColor = source["accentColor"];
	        this.avatar = source["avatar"];
	        this.state = source["state"];
	        this.open = source["open"];
	        this.active = source["active"];
	        this.mark = source["mark"];
	        this.label = source["label"];
	        this.unread = source["unread"];
	        this.working = source["working"];
	    }
	}
	export class ConversationMsg {
	    agent: string;
	    conversationId: string;
	    adopt?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConversationMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversationId = source["conversationId"];
	        this.adopt = source["adopt"];
	    }
	}
	export class ConversationRow {
	    id: string;
	    title: string;
	    turnCount: number;
	    updatedAt: string;
	    current: boolean;
	    unread: boolean;
	    working: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConversationRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.turnCount = source["turnCount"];
	        this.updatedAt = source["updatedAt"];
	        this.current = source["current"];
	        this.unread = source["unread"];
	        this.working = source["working"];
	    }
	}
	export class ConversationsMsg {
	    agent: string;
	    conversations: ConversationRow[];
	
	    static createFrom(source: any = {}) {
	        return new ConversationsMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversations = this.convertValues(source["conversations"], ConversationRow);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Tokens {
	    in: number;
	    out: number;
	
	    static createFrom(source: any = {}) {
	        return new Tokens(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.in = source["in"];
	        this.out = source["out"];
	    }
	}
	export class Entry {
	    agent: string;
	    conversationId: string;
	    role: string;
	    kind: string;
	    title?: string;
	    text: string;
	    html?: string;
	    model?: string;
	    elapsedMs?: number;
	    tokens?: Tokens;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversationId = source["conversationId"];
	        this.role = source["role"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.text = source["text"];
	        this.html = source["html"];
	        this.model = source["model"];
	        this.elapsedMs = source["elapsedMs"];
	        this.tokens = this.convertValues(source["tokens"], Tokens);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HistoryMsg {
	    agent: string;
	    conversationId: string;
	    entries: Entry[];
	    hasMore: boolean;
	    older: boolean;
	
	    static createFrom(source: any = {}) {
	        return new HistoryMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversationId = source["conversationId"];
	        this.entries = this.convertValues(source["entries"], Entry);
	        this.hasMore = source["hasMore"];
	        this.older = source["older"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelRow {
	    name: string;
	    target: string;
	    current: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.target = source["target"];
	        this.current = source["current"];
	    }
	}
	export class ModelsMsg {
	    agent: string;
	    models: ModelRow[];
	
	    static createFrom(source: any = {}) {
	        return new ModelsMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.models = this.convertValues(source["models"], ModelRow);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProgressMsg {
	    agent: string;
	    conversationId?: string;
	    rows: core.ProgressRow[];
	    status?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProgressMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversationId = source["conversationId"];
	        this.rows = this.convertValues(source["rows"], core.ProgressRow);
	        this.status = source["status"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionInfo {
	    agent: string;
	    principal: string;
	    conversationTitle: string;
	    model?: string;
	    protocolVersion: number;
	    displayName?: string;
	    accentColor?: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.principal = source["principal"];
	        this.conversationTitle = source["conversationTitle"];
	        this.model = source["model"];
	        this.protocolVersion = source["protocolVersion"];
	        this.displayName = source["displayName"];
	        this.accentColor = source["accentColor"];
	    }
	}
	export class StateMsg {
	    agent: string;
	    state: string;
	    attempt?: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new StateMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.state = source["state"];
	        this.attempt = source["attempt"];
	        this.error = source["error"];
	    }
	}
	export class ThinkingMsg {
	    agent: string;
	    thinking: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ThinkingMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.thinking = source["thinking"];
	    }
	}
	
	export class UnreadMsg {
	    agent: string;
	    conversations: string[];
	
	    static createFrom(source: any = {}) {
	        return new UnreadMsg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent = source["agent"];
	        this.conversations = source["conversations"];
	    }
	}
	export class schemaAnchor {
	    entry: Entry;
	    session: SessionInfo;
	    state: StateMsg;
	    thinking: ThinkingMsg;
	    agent: AgentInfo;
	    progress: ProgressMsg;
	    models: ModelsMsg;
	    history: HistoryMsg;
	    conversations: ConversationsMsg;
	    conversation: ConversationMsg;
	    unread: UnreadMsg;
	
	    static createFrom(source: any = {}) {
	        return new schemaAnchor(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entry = this.convertValues(source["entry"], Entry);
	        this.session = this.convertValues(source["session"], SessionInfo);
	        this.state = this.convertValues(source["state"], StateMsg);
	        this.thinking = this.convertValues(source["thinking"], ThinkingMsg);
	        this.agent = this.convertValues(source["agent"], AgentInfo);
	        this.progress = this.convertValues(source["progress"], ProgressMsg);
	        this.models = this.convertValues(source["models"], ModelsMsg);
	        this.history = this.convertValues(source["history"], HistoryMsg);
	        this.conversations = this.convertValues(source["conversations"], ConversationsMsg);
	        this.conversation = this.convertValues(source["conversation"], ConversationMsg);
	        this.unread = this.convertValues(source["unread"], UnreadMsg);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

