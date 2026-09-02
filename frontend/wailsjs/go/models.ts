export namespace models {
	
	export class CallEntry {
	    id: string;
	    peerId: string;
	    name: string;
	    direction: string;
	    outcome: string;
	    video: boolean;
	    duration: number;
	    ts: number;
	
	    static createFrom(source: any = {}) {
	        return new CallEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.peerId = source["peerId"];
	        this.name = source["name"];
	        this.direction = source["direction"];
	        this.outcome = source["outcome"];
	        this.video = source["video"];
	        this.duration = source["duration"];
	        this.ts = source["ts"];
	    }
	}
	export class Chat {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
	    background: string;
	    pattern: string;
	    accountDeleted: boolean;
	    lastMessage: string;
	    lastTimestamp: number;
	    unread: number;
	
	    static createFrom(source: any = {}) {
	        return new Chat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peerId = source["peerId"];
	        this.name = source["name"];
	        this.username = source["username"];
	        this.bio = source["bio"];
	        this.avatar = source["avatar"];
	        this.background = source["background"];
	        this.pattern = source["pattern"];
	        this.accountDeleted = source["accountDeleted"];
	        this.lastMessage = source["lastMessage"];
	        this.lastTimestamp = source["lastTimestamp"];
	        this.unread = source["unread"];
	    }
	}
	export class Message {
	    id: string;
	    chatId: string;
	    senderId: string;
	    text: string;
	    mediaKind?: string;
	    mediaData?: string;
	    ts: number;
	    deletedForMe: boolean;
	    deletedForBoth: boolean;
	    read: boolean;
	    reaction?: string;
	    reactionPeer?: string;
	    delivered: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.chatId = source["chatId"];
	        this.senderId = source["senderId"];
	        this.text = source["text"];
	        this.mediaKind = source["mediaKind"];
	        this.mediaData = source["mediaData"];
	        this.ts = source["ts"];
	        this.deletedForMe = source["deletedForMe"];
	        this.deletedForBoth = source["deletedForBoth"];
	        this.read = source["read"];
	        this.reaction = source["reaction"];
	        this.reactionPeer = source["reactionPeer"];
	        this.delivered = source["delivered"];
	    }
	}
	export class Peer {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
	    background: string;
	    pattern: string;
	    ip: string;
	    port: number;
	    lastSeen: number;
	    viaVpn?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Peer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peerId = source["peerId"];
	        this.name = source["name"];
	        this.username = source["username"];
	        this.bio = source["bio"];
	        this.avatar = source["avatar"];
	        this.background = source["background"];
	        this.pattern = source["pattern"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.lastSeen = source["lastSeen"];
	        this.viaVpn = source["viaVpn"];
	    }
	}
	export class Profile {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
	    background: string;
	    pattern: string;
	    createdAt: number;
	
	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peerId = source["peerId"];
	        this.name = source["name"];
	        this.username = source["username"];
	        this.bio = source["bio"];
	        this.avatar = source["avatar"];
	        this.background = source["background"];
	        this.pattern = source["pattern"];
	        this.createdAt = source["createdAt"];
	    }
	}

}

export namespace vpn {
	
	export class Member {
	    peerId: string;
	    name: string;
	    username: string;
	    pubKey: string;
	    isHost: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Member(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peerId = source["peerId"];
	        this.name = source["name"];
	        this.username = source["username"];
	        this.pubKey = source["pubKey"];
	        this.isHost = source["isHost"];
	    }
	}
	export class Status {
	    active: boolean;
	    role: string;
	    network: string;
	    members: Member[];
	    invite: string;
	    transport: string;
	    relayAddr: string;
	    listenPort: number;
	    publicAddr: string;
	    portMapped: boolean;
	    fingerprint: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.role = source["role"];
	        this.network = source["network"];
	        this.members = this.convertValues(source["members"], Member);
	        this.invite = source["invite"];
	        this.transport = source["transport"];
	        this.relayAddr = source["relayAddr"];
	        this.listenPort = source["listenPort"];
	        this.publicAddr = source["publicAddr"];
	        this.portMapped = source["portMapped"];
	        this.fingerprint = source["fingerprint"];
	        this.error = source["error"];
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

