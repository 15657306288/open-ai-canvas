import crypto, { type JsonWebKey as NodeJsonWebKey } from "node:crypto";

import {
    canonicalRuntimeJson,
    createRuntimeRequestPayload,
    sha256Base64Url,
    type LocalRuntimeScope,
    type RuntimeSessionChallengePayload,
} from "./local-runtime-contract.js";

const REGISTERED_CHALLENGE_TTL_MS = 60_000;
// [connector] P0-A 稳定性：默认值放宽并全部可配置。对外 expiresAt 不再硬性 10min，
// 内部过期时刻随活跃请求滑动续期（签名载荷仍使用对外 expiresAt，协议零破坏）。
const DEFAULT_SESSION_TTL_MS = 30 * 60_000;
const DEFAULT_SESSION_ABSOLUTE_TTL_MS = 12 * 60 * 60_000;
const DEFAULT_REQUEST_CLOCK_SKEW_MS = 60_000;
const DEFAULT_MAX_SESSION_NONCES = 2_048;
const MAX_PENDING_CHALLENGES = 64;

export const LOCAL_RUNTIME_DEFAULT_SCOPES: readonly LocalRuntimeScope[] = [
    "runtime:status",
    "runtime:revoke",
    "canvas:connect",
    "dreamina:status",
    "dreamina:login",
    "dreamina:logout",
    "dreamina:run",
    "dreamina:models",
    "dreamina:generate",
];

export type RuntimeBrowserRegistration = {
    keyId: string;
    origin: string;
    publicKeyJwk: JsonWebKey;
    fingerprint: string;
    createdAt: string;
};

export type RuntimePublicSession = {
    sessionId: string;
    keyId: string;
    scopes: readonly LocalRuntimeScope[];
    expiresAt: string;
};

export type RuntimeChallengeResponse = {
    state: "challenge";
    challengeId: string;
    nonce: string;
    runtimeInstanceId: string;
    expiresAt: string;
    keyId: string;
};

export type RuntimeRequestVerification = {
    sessionId: string;
    origin: string;
    method: string;
    pathAndQuery: string;
    body: Uint8Array;
    lastEventId: string | null;
    requestNonce: string;
    timestamp: number;
    proof: string;
    scope: LocalRuntimeScope;
};

type PendingChallenge = {
    challengeId: string;
    nonce: string;
    origin: string;
    keyId: string;
    publicKeyJwk: JsonWebKey;
    fingerprint: string;
    expiresAt: number;
};

type RuntimeSessionRecord = RuntimePublicSession & {
    origin: string;
    // [connector] P0-A 稳定性：createdAtMs 用于绝对上限（12h 强制重握手）；
    // usedNonces 由 Set 改 Map（nonce -> ts），Map 保持插入顺序，超限时 LRU 清理最旧项。
    createdAtMs: number;
    expiresAtMs: number;
    usedNonces: Map<string, number>;
};

export type LocalRuntimeSessionManagerOptions = {
    endpoint: string;
    runtimeInstanceId?: string;
    trustedOrigins: readonly string[];
    registrations: RuntimeBrowserRegistration[];
    persistRegistrations?: (registrations: readonly RuntimeBrowserRegistration[]) => void;
    now?: () => number;
    scopes?: readonly LocalRuntimeScope[];
    onSessionRevoked?: (sessionId: string) => void;
    // [connector] P0-A 稳定性：会话时长/绝对上限/时钟窗/nonce 上限全部可配置
    sessionTtlMs?: number;
    sessionAbsoluteTtlMs?: number;
    requestClockSkewMs?: number;
    maxSessionNonces?: number;
    timers?: {
        setTimeout(callback: () => void, delayMs: number): unknown;
        clearTimeout(handle: unknown): void;
    };
};

export class LocalRuntimeSessionError extends Error {
    constructor(readonly code: string, message = code, readonly statusCode = 403) {
        super(message);
        this.name = "LocalRuntimeSessionError";
    }
}

export class LocalRuntimeSessionManager {
    readonly runtimeInstanceId: string;
    private readonly endpoint: string;
    private readonly trustedOrigins: Set<string>;
    private readonly registrations: RuntimeBrowserRegistration[];
    private readonly persistRegistrations: (registrations: readonly RuntimeBrowserRegistration[]) => void;
    private readonly now: () => number;
    private readonly scopes: readonly LocalRuntimeScope[];
    private readonly onSessionRevoked?: (sessionId: string) => void;
    private readonly timers: NonNullable<LocalRuntimeSessionManagerOptions["timers"]>;
    // [connector] P0-A 稳定性：可配置会话时长/绝对上限/时钟窗/nonce 上限
    private readonly sessionTtlMs: number;
    private readonly sessionAbsoluteTtlMs: number;
    private readonly requestClockSkewMs: number;
    private readonly maxSessionNonces: number;
    private readonly challenges = new Map<string, PendingChallenge>();
    private readonly sessions = new Map<string, RuntimeSessionRecord>();
    private readonly sessionExpiryTimers = new Map<string, unknown>();

    constructor(options: LocalRuntimeSessionManagerOptions) {
        this.endpoint = exactEndpoint(options.endpoint);
        this.runtimeInstanceId = options.runtimeInstanceId ?? randomBase64Url(18);
        this.trustedOrigins = new Set(options.trustedOrigins.map(exactOrigin));
        this.registrations = options.registrations;
        this.persistRegistrations = options.persistRegistrations ?? (() => undefined);
        this.now = options.now ?? Date.now;
        this.scopes = options.scopes ?? LOCAL_RUNTIME_DEFAULT_SCOPES;
        this.onSessionRevoked = options.onSessionRevoked;
        this.timers = options.timers ?? {
            setTimeout(callback, delayMs) {
                const timer = setTimeout(callback, delayMs);
                timer.unref();
                return timer;
            },
            clearTimeout(handle) {
                clearTimeout(handle as NodeJS.Timeout);
            },
        };
        this.sessionTtlMs = options.sessionTtlMs ?? DEFAULT_SESSION_TTL_MS;
        this.sessionAbsoluteTtlMs = options.sessionAbsoluteTtlMs ?? DEFAULT_SESSION_ABSOLUTE_TTL_MS;
        this.requestClockSkewMs = options.requestClockSkewMs ?? DEFAULT_REQUEST_CLOCK_SKEW_MS;
        this.maxSessionNonces = options.maxSessionNonces ?? DEFAULT_MAX_SESSION_NONCES;
    }

    isTrustedOrigin(origin: string) {
        try {
            return this.trustedOrigins.has(exactOrigin(origin));
        } catch {
            return false;
        }
    }

    createChallenge(
        originValue: string,
        input: { publicKeyJwk: JsonWebKey; keyId?: never } | { keyId: string; publicKeyJwk?: never },
    ): RuntimeChallengeResponse {
        const origin = this.requireTrustedOrigin(originValue);
        this.pruneExpired();
        if ("publicKeyJwk" in input && input.publicKeyJwk) {
            const publicKeyJwk = validatePublicJwk(input.publicKeyJwk);
            const keyId = runtimeBrowserKeyId(publicKeyJwk);
            const registered = this.findRegistration(origin, keyId);
            if (registered) return this.createRegisteredChallenge(registered);
            const existing = [...this.challenges.values()].find((challenge) => (
                challenge.origin === origin
                && challenge.keyId === keyId
                && challenge.expiresAt > this.now()
            ));
            if (existing) return this.publicChallenge(existing);
            return this.createSignedChallenge(origin, keyId, publicKeyJwk, keyId);
        }
        if (!("keyId" in input) || typeof input.keyId !== "string") {
            throw new LocalRuntimeSessionError("challenge_invalid", "会话挑战请求无效", 400);
        }
        const registration = this.findRegistration(origin, input.keyId);
        if (!registration) {
            throw new LocalRuntimeSessionError("registration_not_found", "浏览器密钥尚未注册", 404);
        }
        return this.createRegisteredChallenge(registration);
    }

    exchange(originValue: string, input: { challengeId: string; signature: string }): RuntimePublicSession {
        const origin = this.requireTrustedOrigin(originValue);
        const challenge = this.requireChallenge(input.challengeId);
        if (challenge.origin !== origin) {
            this.challenges.delete(challenge.challengeId);
            throw new LocalRuntimeSessionError("challenge_invalid", "会话挑战无效", 403);
        }
        const payload = sessionChallengePayload(
            challenge,
            this.endpoint,
            this.runtimeInstanceId,
        );
        if (!verifyP1363(challenge.publicKeyJwk, canonicalRuntimeJson(payload), input.signature)) {
            this.challenges.delete(challenge.challengeId);
            throw new LocalRuntimeSessionError("challenge_proof_invalid", "会话签名无效", 403);
        }
        this.challenges.delete(challenge.challengeId);
        if (!this.findRegistration(challenge.origin, challenge.keyId)) {
            this.registrations.push({
                keyId: challenge.keyId,
                origin: challenge.origin,
                publicKeyJwk: challenge.publicKeyJwk,
                fingerprint: challenge.fingerprint,
                createdAt: new Date(this.now()).toISOString(),
            });
            this.persistRegistrations(this.registrations);
        }
        return this.createSession(challenge.origin, challenge.keyId);
    }

    verifyRequest(input: RuntimeRequestVerification): RuntimePublicSession {
        const session = this.sessions.get(input.sessionId);
        if (!session || session.expiresAtMs <= this.now()) {
            if (session) this.removeSession(session.sessionId);
            throw new LocalRuntimeSessionError("session_invalid", "本机会话无效", 401);
        }
        const origin = this.requireTrustedOrigin(input.origin);
        if (session.origin !== origin) {
            throw new LocalRuntimeSessionError("session_invalid", "本机会话来源不匹配", 401);
        }
        if (!Number.isSafeInteger(input.timestamp)
            || Math.abs(this.now() - input.timestamp) > this.requestClockSkewMs) {
            throw new LocalRuntimeSessionError("request_stale", "请求时间已失效", 401);
        }
        if (!session.scopes.includes(input.scope)) {
            throw new LocalRuntimeSessionError("scope_denied", "本机会话权限不足", 403);
        }
        if (!validNonce(input.requestNonce)) {
            throw new LocalRuntimeSessionError("request_nonce_invalid", "请求随机数无效", 400);
        }
        if (session.usedNonces.has(input.requestNonce)) {
            throw new LocalRuntimeSessionError("request_replayed", "请求已使用", 409);
        }
        const registration = this.findRegistration(origin, session.keyId);
        if (!registration) {
            this.removeSession(session.sessionId);
            throw new LocalRuntimeSessionError("session_invalid", "本机浏览器授权已撤销", 401);
        }
        const payload = createRuntimeRequestPayload({
            sessionId: session.sessionId,
            keyId: session.keyId,
            method: input.method.toUpperCase(),
            pathAndQuery: input.pathAndQuery,
            bodySha256: sha256Base64Url(input.body),
            lastEventId: input.lastEventId,
            origin,
            endpoint: this.endpoint,
            runtimeInstanceId: this.runtimeInstanceId,
            requestNonce: input.requestNonce,
            timestamp: input.timestamp,
            sessionExpiresAt: session.expiresAt,
        });
        if (!verifyP1363(registration.publicKeyJwk, canonicalRuntimeJson(payload), input.proof)) {
            throw new LocalRuntimeSessionError("request_proof_invalid", "请求签名无效", 401);
        }
        // Signature verification precedes the atomic replay mark, so failed substitutions
        // cannot consume a valid nonce while concurrent identical proofs cannot both pass.
        if (session.usedNonces.has(input.requestNonce)) {
            throw new LocalRuntimeSessionError("request_replayed", "请求已使用", 409);
        }
        // [connector] P0-A 稳定性：nonce 采用 LRU 滑动记录（超上限清理最旧项，不再 429 硬顶）
        this.recordNonce(session, input.requestNonce);
        // [connector] P0-A 稳定性：签名验证通过且剩余不足一半 TTL 时滑动续期。
        // 仅更新内部过期时刻 expiresAtMs 与定时器，对外 expiresAt 保持不变，
        // 因此签名载荷始终一致，对既有客户端协议零破坏。
        this.maybeRenewSession(session);
        return publicSession(session);
    }

    revokeSession(sessionId: string) {
        if (!this.sessions.has(sessionId)) {
            throw new LocalRuntimeSessionError("session_invalid", "本机会话无效", 401);
        }
        this.removeSession(sessionId);
    }

    revokeRegistration(sessionId: string) {
        const session = this.sessions.get(sessionId);
        if (!session) {
            throw new LocalRuntimeSessionError("session_invalid", "本机会话无效", 401);
        }
        const keyId = session.keyId;
        const origin = session.origin;
        for (let index = this.registrations.length - 1; index >= 0; index -= 1) {
            const item = this.registrations[index];
            if (item.keyId === keyId && item.origin === origin) this.registrations.splice(index, 1);
        }
        for (const [challengeId, challenge] of this.challenges) {
            if (challenge.keyId === keyId && challenge.origin === origin) {
                this.challenges.delete(challengeId);
            }
        }
        for (const [activeSessionId, activeSession] of this.sessions) {
            if (activeSession.keyId === keyId && activeSession.origin === origin) {
                this.removeSession(activeSessionId);
            }
        }
        this.persistRegistrations(this.registrations);
    }

    dispose() {
        for (const sessionId of [...this.sessions.keys()]) this.removeSession(sessionId);
        this.challenges.clear();
    }

    private createRegisteredChallenge(registration: RuntimeBrowserRegistration): RuntimeChallengeResponse {
        return this.createSignedChallenge(
            registration.origin,
            registration.keyId,
            validatePublicJwk(registration.publicKeyJwk),
            registration.fingerprint,
        );
    }

    private createSignedChallenge(
        origin: string,
        keyId: string,
        publicKeyJwk: JsonWebKey,
        fingerprint: string,
    ): RuntimeChallengeResponse {
        if (this.challenges.size >= MAX_PENDING_CHALLENGES) {
            throw new LocalRuntimeSessionError("rate_limited", "会话挑战过多", 429);
        }
        const challenge: PendingChallenge = {
            challengeId: randomBase64Url(18),
            nonce: randomBase64Url(32),
            origin,
            keyId,
            publicKeyJwk,
            fingerprint,
            expiresAt: this.now() + REGISTERED_CHALLENGE_TTL_MS,
        };
        this.challenges.set(challenge.challengeId, challenge);
        return this.publicChallenge(challenge);
    }

    private publicChallenge(challenge: PendingChallenge): RuntimeChallengeResponse {
        return {
            state: "challenge",
            challengeId: challenge.challengeId,
            nonce: challenge.nonce,
            runtimeInstanceId: this.runtimeInstanceId,
            expiresAt: new Date(challenge.expiresAt).toISOString(),
            keyId: challenge.keyId,
        };
    }

    private createSession(origin: string, keyId: string): RuntimePublicSession {
        // [connector] P0-A 稳定性：TTL 可配置；记录 createdAtMs 供绝对上限判断
        const now = this.now();
        const expiresAtMs = now + this.sessionTtlMs;
        const session: RuntimeSessionRecord = {
            sessionId: randomBase64Url(24),
            keyId,
            origin,
            scopes: [...this.scopes],
            createdAtMs: now,
            expiresAt: new Date(expiresAtMs).toISOString(),
            expiresAtMs,
            usedNonces: new Map(),
        };
        this.sessions.set(session.sessionId, session);
        this.armExpiryTimer(session.sessionId);
        return publicSession(session);
    }

    // [connector] P0-A 稳定性：滑动续期——活跃会话剩余不足一半 TTL 时，将内部过期时刻
    // 延长到 now + TTL（受绝对上限约束，防止"永不过期"会话）。对外 expiresAt 不变。
    private maybeRenewSession(session: RuntimeSessionRecord) {
        const now = this.now();
        const absoluteExpiresAtMs = session.createdAtMs + this.sessionAbsoluteTtlMs;
        if (now >= absoluteExpiresAtMs) return;
        if (session.expiresAtMs - now >= this.sessionTtlMs / 2) return;
        session.expiresAtMs = Math.min(now + this.sessionTtlMs, absoluteExpiresAtMs);
        this.armExpiryTimer(session.sessionId);
    }

    // [connector] P0-A 稳定性：按内部过期时刻重建到期定时器（续期后重新计时）
    private armExpiryTimer(sessionId: string) {
        const session = this.sessions.get(sessionId);
        if (!session) return;
        const existing = this.sessionExpiryTimers.get(sessionId);
        if (existing !== undefined) this.timers.clearTimeout(existing);
        const remaining = Math.max(1, session.expiresAtMs - this.now());
        this.sessionExpiryTimers.set(
            sessionId,
            this.timers.setTimeout(() => this.removeSession(sessionId), remaining),
        );
    }

    // [connector] P0-A 稳定性：nonce 采用 LRU 滑动记录，超上限时清理最旧项
    private recordNonce(session: RuntimeSessionRecord, nonce: string) {
        session.usedNonces.set(nonce, this.now());
        if (session.usedNonces.size > this.maxSessionNonces) {
            const oldest = session.usedNonces.keys().next().value as string | undefined;
            if (oldest !== undefined) session.usedNonces.delete(oldest);
        }
    }

    private requireTrustedOrigin(value: string) {
        const origin = exactOrigin(value);
        if (!this.trustedOrigins.has(origin)) {
            throw new LocalRuntimeSessionError("origin_not_trusted", "来源未获本机授权", 403);
        }
        return origin;
    }

    private requireChallenge(challengeId: string) {
        const challenge = this.challenges.get(challengeId);
        if (!challenge || challenge.expiresAt <= this.now()) {
            if (challenge) this.challenges.delete(challengeId);
            throw new LocalRuntimeSessionError("challenge_invalid", "会话挑战已失效", 404);
        }
        return challenge;
    }

    private findRegistration(origin: string, keyId: string) {
        return this.registrations.find((registration) => (
            registration.origin === origin && registration.keyId === keyId
        ));
    }

    private removeSession(sessionId: string) {
        if (!this.sessions.delete(sessionId)) return;
        if (this.sessionExpiryTimers.has(sessionId)) {
            const timer = this.sessionExpiryTimers.get(sessionId);
            this.sessionExpiryTimers.delete(sessionId);
            this.timers.clearTimeout(timer);
        }
        this.onSessionRevoked?.(sessionId);
    }

    private pruneExpired() {
        const current = this.now();
        for (const [challengeId, challenge] of this.challenges) {
            if (challenge.expiresAt <= current) this.challenges.delete(challengeId);
        }
        for (const [sessionId, session] of this.sessions) {
            if (session.expiresAtMs <= current) this.removeSession(sessionId);
        }
    }
}

export function runtimeBrowserKeyId(value: JsonWebKey) {
    const jwk = validatePublicJwk(value);
    return sha256Base64Url(canonicalRuntimeJson({
        crv: jwk.crv,
        kty: jwk.kty,
        x: jwk.x,
        y: jwk.y,
    }));
}

function validatePublicJwk(value: JsonWebKey) {
    if (!value || typeof value !== "object") {
        throw new LocalRuntimeSessionError("invalid_public_key", "浏览器公钥无效", 400);
    }
    const allowed = new Set(["kty", "crv", "x", "y", "ext", "key_ops"]);
    if (Object.keys(value).some((key) => !allowed.has(key))
        || value.kty !== "EC"
        || value.crv !== "P-256"
        || typeof value.x !== "string"
        || typeof value.y !== "string"
        || !/^[A-Za-z0-9_-]{43}$/.test(value.x)
        || !/^[A-Za-z0-9_-]{43}$/.test(value.y)
        || value.d !== undefined
        || value.ext !== true
        || !Array.isArray(value.key_ops)
        || value.key_ops.length !== 1
        || value.key_ops[0] !== "verify") {
        throw new LocalRuntimeSessionError("invalid_public_key", "浏览器公钥无效", 400);
    }
    try {
        crypto.createPublicKey({ key: value as NodeJsonWebKey, format: "jwk" });
    } catch {
        throw new LocalRuntimeSessionError("invalid_public_key", "浏览器公钥无效", 400);
    }
    return {
        kty: value.kty,
        crv: value.crv,
        x: value.x,
        y: value.y,
        ext: true,
        key_ops: ["verify"],
    } satisfies JsonWebKey;
}

function sessionChallengePayload(
    challenge: PendingChallenge,
    endpoint: string,
    runtimeInstanceId: string,
): RuntimeSessionChallengePayload {
    return {
        protocol: "framefield-runtime-session-v1",
        challengeId: challenge.challengeId,
        nonce: challenge.nonce,
        origin: challenge.origin,
        endpoint,
        runtimeInstanceId,
        expiresAt: new Date(challenge.expiresAt).toISOString(),
    };
}

function verifyP1363(publicKeyJwk: JsonWebKey, payload: string, signatureValue: string) {
    if (!/^[A-Za-z0-9_-]{86}$/.test(signatureValue)) return false;
    const signature = Buffer.from(signatureValue, "base64url");
    if (signature.byteLength !== 64) return false;
    try {
        const publicKey = crypto.createPublicKey({
            key: publicKeyJwk as NodeJsonWebKey,
            format: "jwk",
        });
        return crypto.verify("sha256", Buffer.from(payload), {
            key: publicKey,
            dsaEncoding: "ieee-p1363",
        }, signature);
    } catch {
        return false;
    }
}

function publicSession(session: RuntimeSessionRecord): RuntimePublicSession {
    return {
        sessionId: session.sessionId,
        keyId: session.keyId,
        scopes: [...session.scopes],
        expiresAt: session.expiresAt,
    };
}

function randomBase64Url(bytes: number) {
    return crypto.randomBytes(bytes).toString("base64url");
}

function validNonce(value: string) {
    if (!/^[A-Za-z0-9_-]{22,}$/.test(value)) return false;
    try {
        return Buffer.from(value, "base64url").byteLength >= 16;
    } catch {
        return false;
    }
}

function exactOrigin(value: string) {
    if (typeof value !== "string" || value.includes(",") || value === "null") {
        throw new LocalRuntimeSessionError("origin_invalid", "来源无效", 403);
    }
    let url: URL;
    try {
        url = new URL(value);
    } catch {
        throw new LocalRuntimeSessionError("origin_invalid", "来源无效", 403);
    }
    if (!['http:', 'https:'].includes(url.protocol)
        || url.username
        || url.password
        || url.pathname !== "/"
        || url.search
        || url.hash
        || url.origin !== value) {
        throw new LocalRuntimeSessionError("origin_invalid", "来源无效", 403);
    }
    return url.origin;
}

function exactEndpoint(value: string) {
    const origin = exactOrigin(value);
    const url = new URL(origin);
    if (url.hostname !== "127.0.0.1") {
        throw new Error("Local Runtime endpoint must use 127.0.0.1");
    }
    return origin;
}
