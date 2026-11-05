#include <zephyr/kernel.h>
#include <zephyr/bluetooth/bluetooth.h>
#include <zephyr/bluetooth/conn.h>
#include <zephyr/logging/log.h>
#include "g1_netcore.h"

LOG_MODULE_REGISTER(g1min, LOG_LEVEL_INF);

#define SMP_SVC_UUID 0x84, 0xaa, 0x60, 0x74, 0x52, 0x8a, 0x8b, 0x86, \
		     0xd3, 0x4c, 0xb7, 0x1d, 0x1d, 0xdc, 0x53, 0x8d

static const struct bt_data ad[] = {
	BT_DATA_BYTES(BT_DATA_FLAGS, (BT_LE_AD_GENERAL | BT_LE_AD_NO_BREDR)),
	BT_DATA(BT_DATA_NAME_COMPLETE, CONFIG_BT_DEVICE_NAME, sizeof(CONFIG_BT_DEVICE_NAME) - 1),
};

static const struct bt_data sd[] = {
	BT_DATA_BYTES(BT_DATA_UUID128_ALL, SMP_SVC_UUID),
};

static void adv_restart(struct k_work *work);
static K_WORK_DELAYABLE_DEFINE(adv_work, adv_restart);

static void connected(struct bt_conn *conn, uint8_t err)
{
	LOG_INF("connected (err %u)", err);
}

static void disconnected(struct bt_conn *conn, uint8_t reason)
{
	LOG_INF("disconnected (reason 0x%02x)", reason);
	k_work_schedule(&adv_work, K_MSEC(200));
}

BT_CONN_CB_DEFINE(conn_cb) = {
	.connected = connected,
	.disconnected = disconnected,
};

/* Advertising restarts only when not connected (BT_MAX_CONN is 1); a failed start is retried. */
static void adv_restart(struct k_work *work)
{
	int err = bt_le_adv_start(BT_LE_ADV_CONN, ad, ARRAY_SIZE(ad), sd, ARRAY_SIZE(sd));

	if (err && err != -EALREADY) {
		LOG_ERR("advertising failed: %d, retrying", err);
		k_work_schedule(&adv_work, K_SECONDS(1));
	}
}

int main(void)
{
	int err;

	LOG_INF("g1min %s booting", CONFIG_MCUBOOT_IMGTOOL_SIGN_VERSION);
	err = g1_netcore_handshake(1, 34, 0x41, 0x42);
	if (err) {
		LOG_ERR("net core handshake failed: %d", err);
	}
	for (int i = 0; i < 5; i++) {
		k_sleep(K_MSEC(800));
		err = bt_enable(NULL);
		if (!err || err == -EALREADY) {
			break;
		}
		LOG_WRN("bt_enable: %d, retrying", err);
	}
	if (err && err != -EALREADY) {
		LOG_ERR("bluetooth unavailable");
		return 0;
	}
	k_work_schedule(&adv_work, K_NO_WAIT);
	LOG_INF("advertising as %s, SMP ready", CONFIG_BT_DEVICE_NAME);
	return 0;
}
