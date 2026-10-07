/** A supplied logger is the only logging sink; the default is silent. */
export interface Logger {
	info(message: string, detail?: unknown): void;
	warn(message: string, detail?: unknown): void;
}
/** No implicit STUN service or media transceiver is installed. */
export interface WebRTCConfig extends RTCConfiguration {
	logger?: Logger;
	/** Returns a fresh peer whose lifetime (including setup failure) peerpipe owns. */
	peerConnectionFactory?: (config: RTCConfiguration) => RTCPeerConnection;
	transceivers?: ReadonlyArray<{
		kind: string | MediaStreamTrack;
		init?: RTCRtpTransceiverInit;
	}>;
}

export interface PeerConnectionState {
	connectionState: RTCPeerConnectionState;
	iceConnectionState: RTCIceConnectionState;
	iceGatheringState: RTCIceGatheringState;
	signalingState: RTCSignalingState;
}

export type DataChannelLabel = string;

export interface WebRTCCallbacks {
	/** A notification only: the caller serializes negotiation and handles rejection. */
	onNegotiationNeeded?: () => void;
	onIceCandidate?: (candidate: RTCIceCandidate) => void;
	onIceGatheringStateChange?: (state: RTCIceGatheringState) => void;
	onIceCandidateError?: (event: RTCPeerConnectionIceErrorEvent) => void;
	onConnectionStateChange?: (state: PeerConnectionState) => void;
	onTrack?: (event: RTCTrackEvent) => void;
	onDataChannel?: (channel: RTCDataChannel) => void;
	onDataChannelMessage?: (data: unknown, channel: RTCDataChannel) => void;
	onDataChannelOpen?: (channel: RTCDataChannel) => void;
	onDataChannelClose?: (channel: RTCDataChannel) => void;
	/** Native data-channel errors. Method failures throw/reject instead. */
	onError?: (error: Error, channel?: RTCDataChannel) => void;
}
