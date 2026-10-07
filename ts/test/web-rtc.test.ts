import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WebRTC } from "../src/index";

class FakeRTCPeerConnection {
	connectionState: RTCPeerConnectionState = "new";
	iceConnectionState: RTCIceConnectionState = "new";
	iceGatheringState: RTCIceGatheringState = "new";
	signalingState: RTCSignalingState = "stable";
	localDescription: RTCSessionDescriptionInit | null = null;
	onnegotiationneeded: (() => void) | null = null;
	onicecandidate: ((event: RTCPeerConnectionIceEvent) => void) | null = null;
	onconnectionstatechange: (() => void) | null = null;
	ontrack: ((event: RTCTrackEvent) => void) | null = null;
	ondatachannel: ((event: RTCDataChannelEvent) => void) | null = null;

	private listeners = new Map<string, Set<() => void>>();

	addTransceiver() {}

	addEventListener(event: string, listener: () => void) {
		const listeners = this.listeners.get(event) ?? new Set<() => void>();
		listeners.add(listener);
		this.listeners.set(event, listeners);
	}

	removeEventListener(event: string, listener: () => void) {
		this.listeners.get(event)?.delete(listener);
	}

	dispatch(event: string) {
		for (const listener of this.listeners.get(event) ?? []) {
			listener();
		}
	}

	async createAnswer(): Promise<RTCSessionDescriptionInit> {
		return { type: "answer", sdp: "initial-answer" };
	}

	async setLocalDescription(
		description: RTCSessionDescriptionInit,
	): Promise<void> {
		this.localDescription = description;
	}

	close() {}
}

describe("WebRTC ICE gathering helpers", () => {
	const originalPeerConnection = globalThis.RTCPeerConnection;

	beforeEach(() => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		globalThis.RTCPeerConnection = originalPeerConnection;
	});

	it("resolves immediately when ICE gathering is already complete", async () => {
		const webRTC = WebRTC.create();
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;
		pc.iceGatheringState = "complete";

		await expect(webRTC.waitForIceGatheringComplete()).resolves.toBeUndefined();
	});

	it("waits until ICE gathering completes", async () => {
		const webRTC = WebRTC.create();
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;
		pc.iceGatheringState = "gathering";

		const pending = webRTC.waitForIceGatheringComplete();
		setTimeout(() => {
			pc.iceGatheringState = "complete";
			pc.localDescription = { type: "answer", sdp: "gathered-answer" };
			pc.dispatch("icegatheringstatechange");
		}, 0);

		await expect(pending).resolves.toBeUndefined();
	});

	it("times out when ICE gathering never completes", async () => {
		const webRTC = WebRTC.create();
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;
		pc.iceGatheringState = "gathering";

		await expect(webRTC.waitForIceGatheringComplete(10)).rejects.toThrow(
			"ICE gathering timed out",
		);
	});

	it("falls back to the current local description when answer gathering times out", async () => {
		const webRTC = WebRTC.create();
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;
		pc.iceGatheringState = "gathering";

		await expect(
			webRTC.createAnswerWithGatheredIce(undefined, 10),
		).resolves.toEqual({
			type: "answer",
			sdp: "initial-answer",
		});
	});
});

/** EventTarget keeps the consumer's property handlers separate from listeners. */
class FakeDataChannel extends EventTarget {
	onclose: (() => void) | null = null;
	onmessage: ((event: MessageEvent) => void) | null = null;
	constructor(readonly label: string) {
		super();
	}
	close() {
		this.dispatchEvent(new Event("close"));
		this.onclose?.();
	}
	message(data: unknown) {
		const event = new MessageEvent("message", { data });
		this.dispatchEvent(event);
		this.onmessage?.(event);
	}
}
function fakeDataChannel(label: string): FakeDataChannel {
	return new FakeDataChannel(label);
}

describe("WebRTC data channel map", () => {
	const originalPeerConnection = globalThis.RTCPeerConnection;

	beforeEach(() => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		globalThis.RTCPeerConnection = originalPeerConnection;
	});

	it("keeps a re-opened channel when the one it replaced closes late", () => {
		// A replaced subscription may finish closing after its replacement opens.
		const onDataChannelClose = vi.fn();
		const webRTC = WebRTC.create(undefined, { onDataChannelClose });
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;

		const replaced = fakeDataChannel("updates");
		const live = fakeDataChannel("updates");
		pc.ondatachannel?.({ channel: replaced } as unknown as RTCDataChannelEvent);
		pc.ondatachannel?.({ channel: live } as unknown as RTCDataChannelEvent);

		replaced.close();

		expect(webRTC.getDataChannel("updates")).toBe(live);
		// Still reported: consumers receive the actual channel that closed.
		expect(onDataChannelClose).toHaveBeenCalledWith(replaced);

		live.close();

		expect(webRTC.getDataChannel("updates")).toBeUndefined();
	});
});

describe("portable configuration and shutdown", () => {
	afterEach(() => vi.unstubAllGlobals());
	it("passes RTC configuration and installs no implicit video or public STUN", () => {
		const transceiver = vi.fn();
		const construct = vi.fn();
		vi.stubGlobal(
			"RTCPeerConnection",
			class extends FakeRTCPeerConnection {
				constructor(config: RTCConfiguration) {
					super();
					construct(config);
				}
				addTransceiver = transceiver;
			},
		);
		WebRTC.create({ iceServers: [], iceTransportPolicy: "relay" });
		expect(construct).toHaveBeenCalledWith({
			iceServers: [],
			iceTransportPolicy: "relay",
		});
		expect(transceiver).not.toHaveBeenCalled();
	});
	it("rejects an ICE waiter immediately on close", async () => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
		const peer = WebRTC.create();
		const pending = peer.waitForIceGatheringComplete(60000);
		peer.close();
		await expect(pending).rejects.toThrow("Peer connection is closed");
		peer.close();
	});
	it("clears answer-in-progress state after a rejected remote answer", async () => {
		vi.stubGlobal(
			"RTCPeerConnection",
			class extends FakeRTCPeerConnection {
				signalingState: RTCSignalingState = "have-local-offer";
				async setRemoteDescription() {
					throw new Error("bad answer");
				}
			},
		);
		const peer = WebRTC.create();
		await expect(
			peer.setRemoteDescription({ type: "answer", sdp: "bad" }),
		).rejects.toThrow("bad answer");
		expect(peer.isReadyForOffer()).toBe(false);
	});
});

describe("gathered descriptions during shutdown", () => {
	afterEach(() => vi.unstubAllGlobals());
	it("does not return a usable-looking answer after close cancels gathering", async () => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
		const peer = WebRTC.create();
		const pending = peer.createAnswerWithGatheredIce(undefined, 60000);
		// Allow creation/setLocalDescription to settle and enter gathering.
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		peer.close();
		await expect(pending).rejects.toThrow("Peer connection is closed");
	});
});

describe("transport contracts", () => {
	afterEach(() => vi.unstubAllGlobals());

	it("forwards regular candidates and every native end-of-candidates form", async () => {
		const add = vi.fn().mockResolvedValue(undefined);
		vi.stubGlobal(
			"RTCPeerConnection",
			class extends FakeRTCPeerConnection {
				addIceCandidate = add;
			},
		);
		const peer = WebRTC.create();
		const candidate = { candidate: "candidate:example", sdpMid: "0" };
		const end = { candidate: "", sdpMid: "0" };
		await peer.addIceCandidate(candidate);
		await peer.addIceCandidate(end);
		await peer.addIceCandidate({});
		await peer.addIceCandidate(null);
		await peer.addIceCandidate();
		expect(add.mock.calls).toEqual([
			[candidate],
			[end],
			[{}],
			[null],
			[undefined],
		]);
		peer.close();
	});

	it("closes a partially configured peer and preserves the creation error", () => {
		const failure = new TypeError("invalid transceiver");
		const close = vi.fn();
		const add = vi
			.fn()
			.mockImplementationOnce(() => ({}))
			.mockImplementationOnce(() => {
				throw failure;
			});
		const logger = { info: vi.fn(), warn: vi.fn() };
		vi.stubGlobal(
			"RTCPeerConnection",
			class extends FakeRTCPeerConnection {
				addTransceiver = add;
				close = close;
			},
		);
		expect(() =>
			WebRTC.create({
				logger,
				transceivers: [{ kind: "audio" }, { kind: "invalid" }],
			}),
		).toThrow(failure);
		expect(add).toHaveBeenCalledTimes(2);
		expect(close).toHaveBeenCalledTimes(1);
		expect(logger.warn).toHaveBeenCalledWith("[WebRTC] create:failed", {
			error: failure,
		});
	});

	it("delivers native channel errors with their channel and the latest callback", () => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
		const old = vi.fn();
		const onError = vi.fn();
		const logger = { info: vi.fn(), warn: vi.fn() };
		const peer = WebRTC.create({ logger }, { onError: old });
		const pc = (peer as unknown as { pc: FakeRTCPeerConnection }).pc;
		const channel = fakeDataChannel("updates");
		pc.ondatachannel?.({ channel } as unknown as RTCDataChannelEvent);
		peer.updateCallbacks({ onError });
		const error = new Error("transport failed");
		const event = new Event("error");
		Object.defineProperty(event, "error", { value: error });
		channel.dispatchEvent(event);
		expect(old).not.toHaveBeenCalled();
		expect(onError).toHaveBeenCalledWith(error, channel);
		expect(logger.warn).toHaveBeenCalledWith("[WebRTC] dataChannel:error", {
			label: "updates",
			id: undefined,
			error,
		});
		channel.close();
		channel.dispatchEvent(event);
		expect(onError).toHaveBeenCalledTimes(1);
		peer.close();
	});

	it("keeps method failures in their promise rather than reporting them twice", async () => {
		const failure = new Error("invalid candidate");
		vi.stubGlobal(
			"RTCPeerConnection",
			class extends FakeRTCPeerConnection {
				async addIceCandidate() {
					throw failure;
				}
			},
		);
		const onError = vi.fn();
		const peer = WebRTC.create(undefined, { onError });
		await expect(peer.addIceCandidate({ candidate: "bad" })).rejects.toBe(
			failure,
		);
		expect(onError).not.toHaveBeenCalled();
		peer.close();
	});

	it("composes consumer property handlers with library delivery and cleanup", () => {
		vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
		const onDataChannelMessage = vi.fn();
		const onDataChannelClose = vi.fn();
		const logger = { info: vi.fn(), warn: vi.fn() };
		const peer = WebRTC.create(
			{ logger },
			{ onDataChannelMessage, onDataChannelClose },
		);
		const pc = (peer as unknown as { pc: FakeRTCPeerConnection }).pc;
		const channel = fakeDataChannel("updates");
		pc.ondatachannel?.({ channel } as unknown as RTCDataChannelEvent);
		channel.onmessage = vi.fn();
		channel.onclose = vi.fn();
		channel.message("private-payload");
		expect(onDataChannelMessage).toHaveBeenCalledWith(
			"private-payload",
			channel,
		);
		expect(channel.onmessage).toHaveBeenCalledTimes(1);
		channel.close();
		expect(onDataChannelClose).toHaveBeenCalledWith(channel);
		expect(channel.onclose).toHaveBeenCalledTimes(1);
		expect(peer.getDataChannel("updates")).toBeUndefined();
		channel.message("late");
		expect(onDataChannelMessage).toHaveBeenCalledTimes(1);
		peer.close();
		peer.close();
		expect(
			logger.info.mock.calls.filter(
				([message]) => message === "[WebRTC] closed",
			),
		).toHaveLength(1);
		expect(JSON.stringify(logger.info.mock.calls)).not.toContain(
			"private-payload",
		);
	});
});

describe("ICE timeout validation", () => {
	beforeEach(() => vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection));
	afterEach(() => vi.unstubAllGlobals());
	it.each([
		-1,
		NaN,
		Infinity,
		2 ** 31,
	])("rejects invalid timeout %s without starting negotiation", async (timeout) => {
		const peer = WebRTC.create();
		const pc = (peer as unknown as { pc: FakeRTCPeerConnection }).pc;
		await expect(
			peer.waitForIceGatheringComplete(timeout),
		).rejects.toBeInstanceOf(RangeError);
		await expect(
			peer.createOfferWithGatheredIce(undefined, timeout),
		).rejects.toBeInstanceOf(RangeError);
		await expect(
			peer.createAnswerWithGatheredIce(undefined, timeout),
		).rejects.toBeInstanceOf(RangeError);
		expect(pc.localDescription).toBeNull();
		peer.close();
	});
});
