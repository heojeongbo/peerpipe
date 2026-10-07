import { test, expect } from "@playwright/test";
test("Go pump sends ordered zlib telemetry to the browser across reconnects", async ({
	page,
}) => {
	const errors: string[] = [];
	page.on("pageerror", (e) => errors.push(e.message));
	await page.goto("/");
	for (let attempt = 0; attempt < 2; attempt++) {
		await page.getByRole("button", { name: "Connect", exact: true }).click();
		await expect(page.locator("#state")).toHaveText("connected");
		await expect
			.poll(async () => Number(await page.locator("#count").textContent()))
			.toBeGreaterThan(5);
		await page.getByRole("button", { name: "Close", exact: true }).click();
		await expect(page.locator("#state")).toHaveText("closed");
	}
	expect(errors).toEqual([]);
});
