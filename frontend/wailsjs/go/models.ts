export namespace models {
	
	export class Chat {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
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
	    }
	}
	export class Peer {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
	    ip: string;
	    port: number;
	    lastSeen: number;
	
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
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.lastSeen = source["lastSeen"];
	    }
	}
	export class Profile {
	    peerId: string;
	    name: string;
	    username: string;
	    bio: string;
	    avatar: string;
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
	        this.createdAt = source["createdAt"];
	    }
	}

}

