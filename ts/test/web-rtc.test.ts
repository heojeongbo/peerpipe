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

/** Just enough of an RTCDataChannel for WebRTC to hang its handlers on. */
type FakeDataChannel = {
	label: string;
	onopen: (() => void) | null;
	onclose: (() => void) | null;
	onmessage: ((event: MessageEvent) => void) | null;
};

function fakeDataChannel(label: string): FakeDataChannel {
	return { label, onopen: null, onclose: null, onmessage: null };
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
		// Regression: the close handler deleted by label, so an old robot/teleop
		// finishing its close after the re-granted one arrived dropped the live
		// channel from the map, and the session reported teleop as gone.
		const onDataChannelClose = vi.fn();
		const webRTC = WebRTC.create(undefined, { onDataChannelClose });
		const pc = (webRTC as unknown as { pc: FakeRTCPeerConnection }).pc;

		const replaced = fakeDataChannel("robot/teleop");
		const live = fakeDataChannel("robot/teleop");
		pc.ondatachannel?.({ channel: replaced } as unknown as RTCDataChannelEvent);
		pc.ondatachannel?.({ channel: live } as unknown as RTCDataChannelEvent);

		replaced.onclose?.();

		expect(webRTC.getDataChannel("robot/teleop")).toBe(live);
		// Still reported: the requester reads the map to decide the grant is kept.
		expect(onDataChannelClose).toHaveBeenCalledWith(replaced);

		live.onclose?.();

		expect(webRTC.getDataChannel("robot/teleop")).toBeUndefined();
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
