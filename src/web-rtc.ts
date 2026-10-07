import type {
	DataChannelLabel,
	PeerConnectionState,
	WebRTCCallbacks,
	WebRTCConfig,
	Logger,
} from "./types.js";

export class WebRTC {
	// Cap for waiting on full ICE gathering before sending offer/answer. Must be
	// long enough to gather a TURN relay candidate over the internet (remote dev);
	// 500ms truncated to host/mDNS only, making the first negotiation flaky.
	// Early-returns when gathering completes, so LAN stays fast.
	static readonly ICE_GATHERING_TIMEOUT_MS = 2000;
	protected pc: RTCPeerConnection;
	protected callbacks: WebRTCCallbacks;
	private dataChannels: Map<string, RTCDataChannel> = new Map();
	private makingOffer = false;
	private readonly logger: Logger;
	private closed = false;
	private iceWaiters = new Set<() => void>();
	private isSettingRemoteAnswerPending = false;

	static create(config?: WebRTCConfig, callbacks?: WebRTCCallbacks): WebRTC {
		return new WebRTC(config, callbacks);
	}

	private constructor(config?: WebRTCConfig, callbacks?: WebRTCCallbacks) {
		this.callbacks = callbacks || {};

		const { logger, transceivers, ...rtcConfig } = config ?? {};
		this.logger = logger ?? { info() {}, warn() {} };
		this.pc = new RTCPeerConnection(rtcConfig);
		for (const entry of transceivers ?? []) {
			this.pc.addTransceiver(entry.kind, entry.init);
		}

		this.setupPeerConnectionListeners();
	}

	updateCallbacks(newCallbacks: Partial<WebRTCCallbacks>): void {
		this.callbacks = { ...this.callbacks, ...newCallbacks };
	}

	private setupPeerConnectionListeners(): void {
		this.pc.onnegotiationneeded = () => {
			this.logger.info("[WebRTC] onnegotiationneeded", {
				...this.getConnectionState(),
				makingOffer: this.makingOffer,
				readyForOffer: this.isReadyForOffer(),
			});
		};

		this.pc.onicecandidate = (event) => {
			if (event.candidate) {
				this.callbacks.onIceCandidate?.(event.candidate);
			}
		};

		this.pc.onconnectionstatechange = () => {
			this.callbacks.onConnectionStateChange?.(this.getConnectionState());
		};

		this.pc.ontrack = (event) => {
			this.callbacks.onTrack?.(event);
		};

		this.pc.ondatachannel = (event) => {
			const channel = event.channel;
			this.setupDataChannelListeners(channel);
			this.dataChannels.set(channel.label, channel);
			this.callbacks.onDataChannel?.(channel);
		};
	}

	private setupDataChannelListeners(channel: RTCDataChannel): void {
		channel.onopen = () => {
			this.callbacks.onDataChannelOpen?.(channel);
		};
		channel.onclose = () => {
			// A label can be opened again while its previous channel is still
			// closing (a subscription reopened after cancellation). Deleting by label
			// alone let that late close drop the live replacement, so
			// getDataChannel — and every hasChannel built on it — reported a
			// working channel as gone. The callback still fires for the old one.
			if (this.dataChannels.get(channel.label) === channel) {
				this.dataChannels.delete(channel.label);
			}
			this.callbacks.onDataChannelClose?.(channel);
		};
		channel.onmessage = (event) =>
			this.callbacks.onDataChannelMessage?.(event.data, channel);
	}

	createDataChannel(
		label: DataChannelLabel,
		options?: RTCDataChannelInit,
	): RTCDataChannel {
		this.logger.info("[WebRTC] createDataChannel", {
			label,
			negotiated: options?.negotiated,
			id: options?.id,
			ordered: options?.ordered,
		});
		const channel = this.pc.createDataChannel(label, options);
		this.dataChannels.set(label, channel);
		this.setupDataChannelListeners(channel);
		return channel;
	}

	async createOffer(
		options?: RTCOfferOptions,
	): Promise<RTCSessionDescriptionInit> {
		this.logger.info("[WebRTC] createOffer:start", this.getConnectionState());
		try {
			this.makingOffer = true;
			const offer = await this.pc.createOffer(options);
			await this.pc.setLocalDescription(offer);
			this.logger.info("[WebRTC] createOffer:done", {
				type: offer.type,
				signalingState: this.pc.signalingState,
				sdpSummary: summarizeSdp(offer.sdp),
			});
			return offer;
		} finally {
			this.makingOffer = false;
		}
	}

	async createAnswer(
		options?: RTCAnswerOptions,
	): Promise<RTCSessionDescriptionInit> {
		this.logger.info("[WebRTC] createAnswer:start", this.getConnectionState());
		const answer = await this.pc.createAnswer(options);
		await this.pc.setLocalDescription(answer);
		this.logger.info("[WebRTC] createAnswer:done", {
			type: answer.type,
			signalingState: this.pc.signalingState,
			sdpSummary: summarizeSdp(answer.sdp),
		});
		return answer;
	}

	async waitForIceGatheringComplete(
		timeoutMs: number = WebRTC.ICE_GATHERING_TIMEOUT_MS,
	): Promise<void> {
		if (this.closed) throw new Error("Peer connection is closed");
		if (this.pc.iceGatheringState === "complete") {
			return;
		}

		await new Promise<void>((resolve, reject) => {
			let timeoutId: ReturnType<typeof setTimeout> | undefined;

			const cleanup = () => {
				this.iceWaiters.delete(cancel);
				this.pc.removeEventListener(
					"icegatheringstatechange",
					handleIceGatheringStateChange,
				);
				if (timeoutId) {
					clearTimeout(timeoutId);
				}
			};

			const cancel = () => {
				cleanup();
				reject(new Error("Peer connection is closed"));
			};
			this.iceWaiters.add(cancel);

			const handleIceGatheringStateChange = () => {
				if (this.pc.iceGatheringState !== "complete") {
					return;
				}
				cleanup();
				resolve();
			};

			timeoutId = setTimeout(() => {
				cleanup();
				reject(new Error("ICE gathering timed out"));
			}, timeoutMs);

			this.pc.addEventListener(
				"icegatheringstatechange",
				handleIceGatheringStateChange,
			);
		});
	}

	async createOfferWithGatheredIce(
		options?: RTCOfferOptions,
		timeoutMs?: number,
	): Promise<RTCSessionDescriptionInit> {
		await this.createOffer(options);
		try {
			await this.waitForIceGatheringComplete(timeoutMs);
		} catch (error) {
			if (this.closed) throw error;
			this.logger.warn("[WebRTC] createOfferWithGatheredIce:timeout", {
				error: error instanceof Error ? error.message : String(error),
				...this.getConnectionState(),
			});
		}
		return this.getLocalDescription();
	}

	async createAnswerWithGatheredIce(
		options?: RTCAnswerOptions,
		timeoutMs?: number,
	): Promise<RTCSessionDescriptionInit> {
		await this.createAnswer(options);
		try {
			await this.waitForIceGatheringComplete(timeoutMs);
		} catch (error) {
			if (this.closed) throw error;
			this.logger.warn("[WebRTC] createAnswerWithGatheredIce:timeout", {
				error: error instanceof Error ? error.message : String(error),
				...this.getConnectionState(),
			});
		}
		return this.getLocalDescription();
	}

	async setRemoteDescription(desc: RTCSessionDescriptionInit): Promise<void> {
		this.logger.info("[WebRTC] setRemoteDescription:start", {
			type: desc.type,
			signalingState: this.pc.signalingState,
			sdpSummary: summarizeSdp(desc.sdp),
		});
		if (desc.type === "answer") {
			this.isSettingRemoteAnswerPending = true;
		}
		try {
			await this.pc.setRemoteDescription(desc);
		} finally {
			this.isSettingRemoteAnswerPending = false;
		}
		this.logger.info("[WebRTC] setRemoteDescription:done", {
			type: desc.type,
			signalingState: this.pc.signalingState,
			sdpSummary: summarizeSdp(desc.sdp),
		});
	}

	async rollback(): Promise<void> {
		this.logger.info("[WebRTC] rollback:start", {
			signalingState: this.pc.signalingState,
		});
		await this.pc.setLocalDescription({ type: "rollback" });
		this.logger.info("[WebRTC] rollback:done", {
			signalingState: this.pc.signalingState,
		});
	}

	isReadyForOffer(): boolean {
		return (
			!this.makingOffer &&
			(this.pc.signalingState === "stable" || this.isSettingRemoteAnswerPending)
		);
	}

	async addIceCandidate(candidate: RTCIceCandidateInit): Promise<void> {
		if (!candidate.candidate) return;
		await this.pc.addIceCandidate(candidate);
	}

	getConnectionState(): PeerConnectionState {
		return {
			connectionState: this.pc.connectionState,
			iceConnectionState: this.pc.iceConnectionState,
			iceGatheringState: this.pc.iceGatheringState,
			signalingState: this.pc.signalingState,
		};
	}

	close(): void {
		if (this.closed) return;
		this.closed = true;
		for (const cancel of this.iceWaiters) cancel();
		this.iceWaiters.clear();
		for (const [_, dc] of this.dataChannels) {
			dc.close();
		}
		this.dataChannels.clear();
		this.pc.close();
	}

	getDataChannel(label: string): RTCDataChannel | undefined {
		return this.dataChannels.get(label);
	}

	private getLocalDescription(): RTCSessionDescriptionInit {
		const localDescription = this.pc.localDescription;
		if (!localDescription) {
			throw new Error("Local description is not set");
		}

		return {
			type: localDescription.type,
			sdp: localDescription.sdp ?? undefined,
		};
	}
}

function summarizeSdp(sdp?: string): {
	media: string[];
	codecs: string[];
	directions: string[];
} | null {
	if (!sdp) {
		return null;
	}

	const lines = sdp.split(/\r?\n/);
	const media = lines.filter((line) => line.startsWith("m="));
	const codecs = lines.filter((line) => line.startsWith("a=rtpmap:"));
	const directions = lines.filter(
		(line) =>
			line === "a=sendrecv" ||
			line === "a=sendonly" ||
			line === "a=recvonly" ||
			line === "a=inactive",
	);

	return {
		media,
		codecs,
		directions,
	};
}
