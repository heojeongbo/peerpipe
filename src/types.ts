/** A supplied logger is the only logging sink; the default is silent. */
export interface Logger {
	info(message: string, detail?: unknown): void;
	warn(message: string, detail?: unknown): void;
}
/** No implicit STUN service or media transceiver is installed. */
export interface WebRTCConfig extends RTCConfiguration {
	logger?: Logger;
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
	onIceCandidate?: (candidate: RTCIceCandidate) => void;
	onConnectionStateChange?: (state: PeerConnectionState) => void;
	onTrack?: (event: RTCTrackEvent) => void;
	onDataChannel?: (channel: RTCDataChannel) => void;
	onDataChannelMessage?: (data: unknown, channel: RTCDataChannel) => void;
	onDataChannelOpen?: (channel: RTCDataChannel) => void;
	onDataChannelClose?: (channel: RTCDataChannel) => void;
	onError?: (error: Error) => void;
}
