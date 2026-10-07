import { defineConfig } from "@playwright/test";
export default defineConfig({
	testDir: "./e2e",
	workers: 1,
	use: {
		baseURL: "http://127.0.0.1:18765",
		headless: true,
		launchOptions: { args: ["--disable-features=WebRtcHideLocalIpsWithMdns"] },
	},
	webServer: {
		command: "go run ./examples/telemetry",
		url: "http://127.0.0.1:18765",
		timeout: 120000,
	},
});
