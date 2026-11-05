/*
 * Handshake expected by the stock G1 net-core image (see g1-fw docs/g1-netcore-ipc-protocol.md):
 * the app must bind an RPMsg endpoint named "ipc0" and answer the net core's role request
 * (01 02 ...) with sub-service 1 / sub-command 0, otherwise the net core never registers
 * "nrf_bt_hci" and reboots itself after ~6 s.
 */
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/ipc/ipc_service.h>
#include <zephyr/logging/log.h>
#include "g1_netcore.h"

LOG_MODULE_REGISTER(g1_netcore, LOG_LEVEL_INF);

static struct ipc_ept ept;
static K_SEM_DEFINE(bound_sem, 0, 1);
static K_SEM_DEFINE(role_req_sem, 0, 1);
static K_SEM_DEFINE(addr_sem, 0, 1);

static void bound_cb(void *priv)
{
	k_sem_give(&bound_sem);
}

static void received_cb(const void *data, size_t len, void *priv)
{
	const uint8_t *d = data;

	if (len < 2 || d[0] != 0x01) {
		return;
	}
	switch (d[1]) {
	case 0x02:
		k_sem_give(&role_req_sem);
		break;
	case 0x03:
		if (len >= 8) {
			LOG_INF("net core BD_ADDR %02x:%02x:%02x:%02x:%02x:%02x",
				d[7], d[6], d[5], d[4], d[3], d[2]);
		}
		k_sem_give(&addr_sem);
		break;
	case 0x06:
		LOG_INF("net core build %.*s", (int)MIN(len - 2, 30), (const char *)&d[2]);
		break;
	default:
		break;
	}
}

static struct ipc_ept_cfg ept_cfg = {
	.name = "ipc0",
	.cb = {
		.bound = bound_cb,
		.received = received_cb,
	},
};

int g1_netcore_handshake(uint8_t role, uint8_t esb_channel, uint8_t esb_master, uint8_t esb_slave)
{
	const struct device *ipc = DEVICE_DT_GET(DT_NODELABEL(ipc0));
	uint8_t msg[16] = {0x01, 0x00, role, 0x00, esb_channel, esb_master, esb_slave};
	int err;

	err = ipc_service_open_instance(ipc);
	if (err && err != -EALREADY) {
		LOG_ERR("open instance: %d", err);
		return err;
	}
	err = ipc_service_register_endpoint(ipc, &ept, &ept_cfg);
	if (err) {
		LOG_ERR("register ipc0: %d", err);
		return err;
	}
	if (k_sem_take(&bound_sem, K_SECONDS(5))) {
		LOG_ERR("ipc0 not bound");
		return -ETIMEDOUT;
	}
	if (k_sem_take(&role_req_sem, K_SECONDS(5))) {
		LOG_ERR("no role request from net core");
		return -ETIMEDOUT;
	}
	err = ipc_service_send(&ept, msg, sizeof(msg));
	if (err < 0) {
		LOG_ERR("role reply: %d", err);
		return err;
	}
	if (k_sem_take(&addr_sem, K_SECONDS(3))) {
		LOG_WRN("no BD_ADDR report after role reply");
	}
	LOG_INF("net core accepted role %u", role);
	return 0;
}
