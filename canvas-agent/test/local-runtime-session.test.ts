import assert from "node:assert/strict";
import crypto, { type KeyObject } from "node:crypto";
import { test } from "node:test";

import {
    LOCAL_RUNTIME_ENDPOINT,
    canonicalRuntimeJson,
    createRuntimeRequestPayload,
    sha256Base64Url,
} from "../src/local-runtime-contract.js";
import {
    LocalRuntimeSessionError,
    LocalRuntimeSessionManager,
    runtimeBrowserKeyId,
    type RuntimeBrowserRegistration,
} from "../src/local-runtime-session.js";

const origin = "http://127.0.0.1:3001";
const now = Date.parse("2026-08-10T00:00:00.000Z");

test("Local Runtime canonical JSON follows the RFC 8785 primitive vector", () => {
    assert.equal(
        canonicalRuntimeJson({ z: "line\nfeed", a: [3, true, null], n: 1e-7 }),
        '{"a":[3,true,null],"n":1e-7,"z":"line\\nfeed"}',
    );
});

test("Local Runtime derives RFC 7638 key ids from only public P-256 coordinates", () => {
    const key = browserKey();
    const withMetadata = { ...key.publicJwk, kid: "ignored", alg: "ES256" };

    assert.equal(
        runtimeBrowserKeyId(key.publicJwk),
        sha256Base64Url(canonicalRuntimeJson({
            crv: "P-256",
            kty: "EC",
            x: key.publicJwk.x,
            y: key.publicJwk.y,
        })),
    );
    assert.throws(
        () => runtimeBrowserKeyId(withMetadata as JsonWebKey),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "invalid_public_key",
    );
});

test("an exact trusted origin silently registers a browser key only after its signed challenge proof", () => {
    const key = browserKey();
    const registrations: RuntimeBrowserRegistration[] = [];
    const manager = runtimeManager(registrations);

    const first = manager.createChallenge(origin, { publicKeyJwk: key.publicJwk });
    const second = manager.createChallenge(origin, { publicKeyJwk: key.publicJwk });
    assert.equal(first.state, "challenge");
    assert.deepEqual(second, first);
    assert.equal(first.expiresAt, new Date(now + 60_000).toISOString());
    assert.equal(JSON.stringify(first).includes("pair"), false);
    assert.equal(registrations.length, 0);

    const session = manager.exchange(origin, {
        challengeId: first.challengeId,
        signature: signSessionChallenge(key.privateKey, first),
    });
    assert.equal(session.keyId, runtimeBrowserKeyId(key.publicJwk));
    assert.equal(registrations.length, 1);
    assert.equal(registrations[0].origin, origin);
});

test("the default trusted-origin session authorizes Dreamina catalog reads and explicit generation routes", () => {
    const key = browserKey();
    const manager = runtimeManager([registration(key.publicJwk)]);
    const session = exchangeRegistered(manager, key);

    assert.equal(session.scopes.includes("dreamina:models"), true);
    assert.equal(session.scopes.includes("dreamina:generate"), true);
    assert.doesNotThrow(() => manager.verifyRequest(signedRequest(key.privateKey, session, {
        method: "GET",
        pathAndQuery: "/dreamina/models",
        body: Buffer.alloc(0),
        scope: "dreamina:models",
    })));
    assert.doesNotThrow(() => manager.verifyRequest(signedRequest(key.privateKey, session, {
        method: "POST",
        pathAndQuery: "/dreamina/generate",
        body: Buffer.from("{}"),
        scope: "dreamina:generate",
    })));
});

test("untrusted or missing origins cannot silently register a browser key", () => {
    const key = browserKey();
    assert.throws(
        () => runtimeManager().createChallenge("https://hostile.example", { publicKeyJwk: key.publicJwk }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "origin_not_trusted",
    );
    assert.throws(
        () => runtimeManager().createChallenge("", { publicKeyJwk: key.publicJwk }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "origin_invalid",
    );
});

test("an invalid challenge proof never persists a trusted browser key", () => {
    const key = browserKey();
    const wrongKey = browserKey();
    const registrations: RuntimeBrowserRegistration[] = [];
    let persisted = 0;
    const manager = new LocalRuntimeSessionManager({
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        persistRegistrations: () => { persisted += 1; },
        now: () => now,
    });
    const challenge = manager.createChallenge(origin, { publicKeyJwk: key.publicJwk });

    assert.throws(
        () => manager.exchange(origin, {
            challengeId: challenge.challengeId,
            signature: signSessionChallenge(wrongKey.privateKey, challenge),
        }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "challenge_proof_invalid",
    );
    assert.equal(registrations.length, 0);
    assert.equal(persisted, 0);
});

test("missing registrations use the neutral browser-key diagnostic", () => {
    const key = browserKey();
    assert.throws(
        () => runtimeManager().createChallenge(origin, { keyId: runtimeBrowserKeyId(key.publicJwk) }),
        (error: unknown) => error instanceof LocalRuntimeSessionError
            && error.code === "registration_not_found"
            && error.message === "浏览器密钥尚未注册",
    );
});

test("registered browser receives a sixty-second standard challenge", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    const manager = runtimeManager(registrations);

    const challenge = manager.createChallenge(origin, { keyId: registrations[0].keyId });

    assert.equal(challenge.state, "challenge");
    assert.equal(challenge.expiresAt, new Date(now + 60_000).toISOString());
    assert.deepEqual(Object.keys(challenge).sort(), ["challengeId", "expiresAt", "keyId", "nonce", "runtimeInstanceId", "state"]);
});

test("valid request proof succeeds exactly once and route substitution is rejected", () => {
    const key = browserKey();
    const manager = runtimeManager([registration(key.publicJwk)]);
    const session = exchangeRegistered(manager, key);
    const input = signedRequest(key.privateKey, session, {
        method: "GET",
        pathAndQuery: "/dreamina/status",
        body: Buffer.alloc(0),
        scope: "dreamina:status",
    });

    const verified = manager.verifyRequest(input);
    assert.equal(verified.sessionId, session.sessionId);
    assert.throws(
        () => manager.verifyRequest(input),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "request_replayed",
    );
    assert.throws(
        () => manager.verifyRequest({ ...signedRequest(key.privateKey, session, {
            method: "GET",
            pathAndQuery: "/dreamina/status",
            body: Buffer.alloc(0),
            scope: "dreamina:status",
        }), pathAndQuery: "/dreamina/logout" }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "request_proof_invalid",
    );
});

test("scope mismatch, stale timestamp, and body substitution fail closed", () => {
    const key = browserKey();
    const manager = runtimeManager([registration(key.publicJwk)], ["dreamina:login"]);
    const session = exchangeRegistered(manager, key);
    const valid = signedRequest(key.privateKey, session, {
        method: "POST",
        pathAndQuery: "/dreamina/login",
        body: Buffer.from("{}"),
        scope: "dreamina:login",
    });

    assert.throws(
        () => manager.verifyRequest({ ...valid, scope: "canvas:connect" }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "scope_denied",
    );
    assert.throws(
        () => manager.verifyRequest({ ...valid, timestamp: now - 61_000 }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "request_stale",
    );
    assert.throws(
        () => manager.verifyRequest({ ...valid, body: Buffer.from('{"changed":true}') }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "request_proof_invalid",
    );
});

test("session and registration revoke remove authorization without accepting another key id", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    const manager = runtimeManager(registrations);
    const session = exchangeRegistered(manager, key);

    manager.revokeSession(session.sessionId);
    assert.throws(
        () => manager.verifyRequest(signedRequest(key.privateKey, session, {
            method: "GET",
            pathAndQuery: "/dreamina/status",
            body: Buffer.alloc(0),
            scope: "dreamina:status",
        })),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "session_invalid",
    );

    const next = exchangeRegistered(manager, key);
    manager.revokeRegistration(next.sessionId);
    assert.equal(registrations.length, 0);
    assert.throws(
        () => manager.createChallenge(origin, { keyId: next.keyId }),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "registration_not_found",
    );
});

test("session absolute TTL schedules revocation without waiting for another request", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    const revoked: string[] = [];
    let expiry: (() => void) | undefined;
    let cleared = 0;
    const options = {
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        now: () => now,
        sessionTtlMs: 10 * 60_000,
        onSessionRevoked: (sessionId: string) => revoked.push(sessionId),
        timers: {
            setTimeout(callback: () => void, delayMs: number) {
                assert.equal(delayMs, 10 * 60_000);
                expiry = callback;
                return "expiry-fixture";
            },
            clearTimeout(handle: unknown) {
                assert.equal(handle, "expiry-fixture");
                cleared += 1;
            },
        },
    } as ConstructorParameters<typeof LocalRuntimeSessionManager>[0];
    const manager = new LocalRuntimeSessionManager(options);
    const session = exchangeRegistered(manager, key);

    assert.equal(typeof expiry, "function");
    expiry?.();

    assert.deepEqual(revoked, [session.sessionId]);
    assert.equal(cleared, 1);
    assert.throws(
        () => manager.verifyRequest(signedRequest(key.privateKey, session, {
            method: "GET",
            pathAndQuery: "/dreamina/status",
            body: Buffer.alloc(0),
            scope: "dreamina:status",
        })),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "session_invalid",
    );
    manager.dispose();
});

function runtimeManager(
    registrations: RuntimeBrowserRegistration[] = [],
    scopes?: ConstructorParameters<typeof LocalRuntimeSessionManager>[0]["scopes"],
) {
    return new LocalRuntimeSessionManager({
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        now: () => now,
        scopes,
    });
}

function browserKey() {
    const { publicKey, privateKey } = crypto.generateKeyPairSync("ec", { namedCurve: "P-256" });
    const exported = publicKey.export({ format: "jwk" });
    const publicJwk: JsonWebKey = {
        kty: exported.kty,
        crv: exported.crv,
        x: exported.x,
        y: exported.y,
        ext: true,
        key_ops: ["verify"],
    };
    return { publicJwk, privateKey };
}

function registration(publicKeyJwk: JsonWebKey): RuntimeBrowserRegistration {
    const keyId = runtimeBrowserKeyId(publicKeyJwk);
    return {
        keyId,
        origin,
        publicKeyJwk,
        fingerprint: keyId,
        createdAt: new Date(now - 1_000).toISOString(),
    };
}

function exchangeRegistered(manager: LocalRuntimeSessionManager, key: ReturnType<typeof browserKey>) {
    const challenge = manager.createChallenge(origin, { keyId: runtimeBrowserKeyId(key.publicJwk) });
    return manager.exchange(origin, {
        challengeId: challenge.challengeId,
        signature: signSessionChallenge(key.privateKey, challenge),
    });
}

function signSessionChallenge(
    privateKey: KeyObject,
    challenge: {
        challengeId: string;
        nonce: string;
        runtimeInstanceId: string;
        expiresAt: string;
    },
) {
    const payload = canonicalRuntimeJson({
        protocol: "framefield-runtime-session-v1",
        challengeId: challenge.challengeId,
        nonce: challenge.nonce,
        origin,
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: challenge.runtimeInstanceId,
        expiresAt: challenge.expiresAt,
    });
    return crypto.sign("sha256", Buffer.from(payload), {
        key: privateKey,
        dsaEncoding: "ieee-p1363",
    }).toString("base64url");
}

function signedRequest(
    privateKey: KeyObject,
    session: { sessionId: string; keyId: string; expiresAt: string },
    request: {
        method: string;
        pathAndQuery: string;
        body: Buffer;
        scope: string;
    },
    timestamp = now,
) {
    const requestNonce = crypto.randomBytes(16).toString("base64url");
    const payload = createRuntimeRequestPayload({
        sessionId: session.sessionId,
        keyId: session.keyId,
        method: request.method,
        pathAndQuery: request.pathAndQuery,
        bodySha256: sha256Base64Url(request.body),
        lastEventId: null,
        origin,
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        requestNonce,
        timestamp,
        sessionExpiresAt: session.expiresAt,
    });
    const proof = crypto.sign("sha256", Buffer.from(canonicalRuntimeJson(payload)), {
        key: privateKey,
        dsaEncoding: "ieee-p1363",
    }).toString("base64url");
    return {
        sessionId: session.sessionId,
        origin,
        method: request.method,
        pathAndQuery: request.pathAndQuery,
        body: request.body,
        lastEventId: null,
        requestNonce,
        timestamp,
        proof,
        scope: request.scope,
    };
}

test("[connector] sliding renewal keeps an active session alive without changing the public expiresAt", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    let current = now;
    let scheduledDelay = 0;
    const manager = new LocalRuntimeSessionManager({
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        now: () => current,
        sessionTtlMs: 60_000,
        sessionAbsoluteTtlMs: 10 * 60_000,
        timers: {
            setTimeout(callback: () => void, delayMs: number) {
                scheduledDelay = delayMs;
                return `t${delayMs}`;
            },
            clearTimeout() { /* noop */ },
        },
    });
    const session = exchangeRegistered(manager, key);
    const publicExpiresAt = session.expiresAt;
    const base = {
        method: "GET" as const,
        pathAndQuery: "/dreamina/status",
        body: Buffer.alloc(0),
        scope: "dreamina:status",
    };
    // 首次请求：剩余恰好 TTL，不触发续期
    manager.verifyRequest(signedRequest(key.privateKey, session, base, current));
    assert.equal(scheduledDelay, 60_000);
    // 走到剩余 20s（< TTL/2=30s）→ 触发续期，内部延长并重新计时
    current = now + 40_000;
    manager.verifyRequest(signedRequest(key.privateKey, session, base, current));
    assert.equal(scheduledDelay, 60_000);
    // 对外 expiresAt 保持不变（保证签名载荷一致性、协议零破坏）
    assert.equal(session.expiresAt, publicExpiresAt);
    // 续期后走到 now+90s（已超过原 60s 过期点）会话仍有效
    current = now + 90_000;
    assert.doesNotThrow(() => manager.verifyRequest(signedRequest(key.privateKey, session, base, current)));
    manager.dispose();
});

test("[connector] sliding renewal respects the absolute session cap", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    let current = now;
    const manager = new LocalRuntimeSessionManager({
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        now: () => current,
        sessionTtlMs: 60_000,
        sessionAbsoluteTtlMs: 120_000,
        timers: {
            setTimeout() { return "t"; },
            clearTimeout() { /* noop */ },
        },
    });
    const session = exchangeRegistered(manager, key);
    const base = {
        method: "GET" as const,
        pathAndQuery: "/dreamina/status",
        body: Buffer.alloc(0),
        scope: "dreamina:status",
    };
    // 多次续期后内部过期时刻顶到绝对上限 120s，不再延长
    for (let offset = 0; offset <= 100; offset += 20) {
        current = now + offset;
        manager.verifyRequest(signedRequest(key.privateKey, session, base, current));
    }
    // 走到 121s：超过绝对上限 → 会话必须失效（强制重新握手）
    current = now + 121_000;
    assert.throws(
        () => manager.verifyRequest(signedRequest(key.privateKey, session, base, current)),
        (error: unknown) => error instanceof LocalRuntimeSessionError && error.code === "session_invalid",
    );
    manager.dispose();
});

test("[connector] nonce sliding cleanup avoids hard rate limits", () => {
    const key = browserKey();
    const registrations = [registration(key.publicJwk)];
    const manager = new LocalRuntimeSessionManager({
        endpoint: LOCAL_RUNTIME_ENDPOINT,
        runtimeInstanceId: "runtime-instance-fixture",
        trustedOrigins: [origin],
        registrations,
        now: () => now,
        maxSessionNonces: 3,
        timers: {
            setTimeout() { return "t"; },
            clearTimeout() { /* noop */ },
        },
    });
    const session = exchangeRegistered(manager, key);
    const base = {
        method: "GET" as const,
        pathAndQuery: "/dreamina/status",
        body: Buffer.alloc(0),
        scope: "dreamina:status",
    };
    // 远超 maxSessionNonces=3 的连续请求也不应触发 rate_limited（LRU 清理最旧）
    for (let i = 0; i < 8; i += 1) {
        assert.doesNotThrow(() => manager.verifyRequest(signedRequest(key.privateKey, session, base)));
    }
    manager.dispose();
});
