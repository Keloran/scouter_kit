/* Self-fallback: if the net core never gives us Bluetooth (HCI over IPC) on several consecutive
 * boots, write Even's embedded, Even-signed net image (G1_NET_IMAGE_NAME, default 1.6.6) into the
 * net-core secondary slot and ask the stock MCUboot to update image 1 on the next boot. MCUboot then
 * takes the usual multi-image/PCD
 * path (RAM-flash bank -> B0N) that every official update uses. See firmware-plan.md §3.3.
 *
 * What the emulator shows: after M boots without BLE the app writes and verifies the net image in the
 * secondary slot and sets image 1 pending; MCUboot then copies it into its RAM bank and starts PCD.
 * What it cannot show: the PCD transfer to the net core and the net-core B0N check (not modelled; the
 * emulated MCUboot waits for the B0N forever, as pcd.c has no timeout); so the fallback is proven up to
 * the point MCUboot hands over to the B0N, not end to end. */
#include <string.h>
#include <zephyr/kernel.h>
#include <zephyr/storage/flash_map.h>
#include <zephyr/dfu/mcuboot.h>
#include <zephyr/logging/log.h>
#include <pm_config.h>
#include "g1.h"
#include "netfallback.h"

LOG_MODULE_REGISTER(netfallback, LOG_LEVEL_INF);

#ifndef G1_NET_IMAGE_NAME
#define G1_NET_IMAGE_NAME "1.6.6"
#endif

extern const uint8_t g1_net_image[];
extern const uint8_t g1_net_image_end[];

#define NET_SLOT_ID PM_MCUBOOT_SECONDARY_1_ID
#define WRITE_BLK 512

static int write_net_image(void)
{
	const struct flash_area *fa;
	size_t len = g1_net_image_end - g1_net_image;
	uint8_t buf[WRITE_BLK];
	int rc;

	if (len == 0 || len > PM_MCUBOOT_SECONDARY_1_SIZE) {
		LOG_ERR("embedded net image size %u does not fit the slot", (unsigned)len);
		return -EINVAL;
	}
	rc = flash_area_open(NET_SLOT_ID, &fa);
	if (rc) {
		LOG_ERR("open net slot: %d", rc);
		return rc;
	}
	rc = flash_area_erase(fa, 0, PM_MCUBOOT_SECONDARY_1_SIZE);
	if (rc) {
		LOG_ERR("erase net slot: %d", rc);
		goto out;
	}
	for (size_t off = 0; off < len; off += WRITE_BLK) {
		size_t n = MIN((size_t)WRITE_BLK, len - off);

		if (n < WRITE_BLK) {
			memset(buf, 0xFF, WRITE_BLK);
		}
		memcpy(buf, &g1_net_image[off], n);
		rc = flash_area_write(fa, off, buf, ROUND_UP(n, 4));
		if (rc) {
			LOG_ERR("write net slot at %u: %d", (unsigned)off, rc);
			goto out;
		}
		wdt0_feed();
	}
	/* verify */
	for (size_t off = 0; off < len; off += WRITE_BLK) {
		size_t n = MIN((size_t)WRITE_BLK, len - off);

		rc = flash_area_read(fa, off, buf, ROUND_UP(n, 4));
		if (rc || memcmp(buf, &g1_net_image[off], n) != 0) {
			LOG_ERR("net slot verify failed at %u (rc %d)", (unsigned)off, rc);
			rc = rc ? rc : -EIO;
			goto out;
		}
		wdt0_feed();
	}
	LOG_INF("net image (%u bytes) written and verified into the net secondary slot", (unsigned)len);
out:
	flash_area_close(fa);
	return rc;
}

/* Pessimistic count: this boot counts as "no Bluetooth" from its start until netfallback_ble_up(),
 * so a boot that ends in any way before BLE is up (handshake error, BT_ASSERT on an HCI timeout,
 * crash, watchdog, the CONFIG_G1_NET_FALLBACK_TIMEOUT_S deadline in health.c) counts. After
 * CONFIG_G1_NET_FALLBACK_BOOTS such boots in a row, the next boot installs the net image first. */
bool netfallback_boot(void)
{
	uint8_t fails = retained_net_fails();

	if (fails >= CONFIG_G1_NET_FALLBACK_BOOTS && !retained_net_fallback_done()) {
		/* one shot, also if setting it up fails: never loop on QSPI or net-core writes */
		retained_set_net_fallback_done();
		LOG_WRN("%u boots in a row without Bluetooth: installing the embedded " G1_NET_IMAGE_NAME " net image via "
			"MCUboot image 1", fails);
		if (write_net_image() != 0) {
			return false;
		}
		/* TEST swap: MCUboot updates the net core on the next boot; the net update itself is
		 * overwrite-only at the B0N. */
		if (boot_request_upgrade_multi(1, BOOT_UPGRADE_TEST) != 0) {
			LOG_ERR("could not set net image 1 pending");
			return false;
		}
		retained_set_net_fails(0);
		LOG_WRN("net image 1 set pending; rebooting into MCUboot for the net-core update");
		return true;
	}
	if (fails < 255) {
		retained_set_net_fails(fails + 1);
	}
	if (fails) {
		LOG_WRN("previous %u boot(s) had no Bluetooth", fails);
	}
	return false;
}

void netfallback_ble_up(void)
{
	retained_set_net_fails(0);
}
