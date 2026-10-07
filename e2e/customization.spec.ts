import { test, expect } from "@playwright/test";

test("native media, custom channels, and remote Go Attach compose", async ({
	page,
}) => {
	await page.goto("/");
	const result = await page.evaluate(async () => {
		// Import the built public artifact, exactly as an independent consumer would.
		const moduleURL = "/dist/index.js";
		const { WebRTC } = await import(moduleURL);
		let negotiations = 0;
		let nativeNegotiations = 0;
		let factoryCalls = 0;
		const peer = WebRTC.create(
			{
				peerConnectionFactory: (config: RTCConfiguration) => {
					factoryCalls++;
					const pc = new RTCPeerConnection(config);
					pc.onnegotiationneeded = () => nativeNegotiations++;
					return pc;
				},
			},
			{ onNegotiationNeeded: () => negotiations++ },
		);
		try {
			// Media configuration remains accessible after construction.
			peer.peerConnection.addTransceiver("audio", { direction: "recvonly" });
			const options = {
				ordered: false,
				maxPacketLifeTime: 500,
				protocol: "peerpipe.echo",
			};
			const channels: RTCDataChannel[] = [
				peer.createDataChannel("files", options),
				peer.createDataChannel("files", options),
			];
			const replies = channels.map(
				(dc, index) =>
					new Promise<number[]>((resolve, reject) => {
						dc.binaryType = "arraybuffer";
						const timer = setTimeout(
							() => reject(new Error("echo timeout")),
							15000,
						);
						dc.onmessage = (event) => {
							clearTimeout(timer);
							resolve([...new Uint8Array(event.data)]);
						};
						dc.onopen = () => dc.send(new Uint8Array([index, 0, 255, 42]));
					}),
			);
			const offer = await peer.createOfferWithGatheredIce();
			const response = await fetch("/offer", {
				method: "POST",
				body: JSON.stringify(offer),
			});
			if (!response.ok) throw new Error(await response.text());
			await peer.setRemoteDescription(await response.json());
			const payloads = await Promise.all(replies);
			const stats: RTCStatsReport = await peer.peerConnection.getStats();
			const beforeRestart = negotiations;
			peer.peerConnection.restartIce();
			await new Promise<void>((resolve, reject) => {
				const timer = setTimeout(
					() => reject(new Error("missing renegotiation")),
					5000,
				);
				peer.updateCallbacks({
					onNegotiationNeeded: () => {
						negotiations++;
						clearTimeout(timer);
						resolve();
					},
				});
			});
			return {
				payloads,
				factoryCalls,
				count: peer.getDataChannels("files").length,
				hasStats: stats.size > 0,
				renegotiated: negotiations > beforeRestart,
				listenersCoexist: nativeNegotiations === negotiations,
				protocols: channels.map((dc) => dc.protocol),
			};
		} finally {
			peer.close();
		}
	});
	expect(result).toEqual({
		payloads: [
			[0, 0, 255, 42],
			[1, 0, 255, 42],
		],
		factoryCalls: 1,
		count: 2,
		hasStats: true,
		renegotiated: true,
		listenersCoexist: true,
		protocols: ["peerpipe.echo", "peerpipe.echo"],
	});
});
