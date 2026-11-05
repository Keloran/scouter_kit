/* The stock G1 net-core image's "ipc0" endpoint (g1-fw docs/g1-netcore-ipc-protocol.md, review-v5.md §8.7): byte 0
 * is a sub-service. 1 (hw-id): the role request (01 02) that must be answered within ~6 s, otherwise the net core
 * never registers "nrf_bt_hci" and reboots itself; its BD_ADDR (01 03), build (01 06), link report (01 07: ESB
 * acknowledged, failed and received counts, the last received signal strength) and the delivery of an ESB frame
 * (01 0d 02). 2 / 4: a 32-byte frame for the net core's ESB slot (the right arm sends it instead of its next
 * announcement, the left returns it in its next acknowledgements); 6 / 4: a frame from the other arm (251 bytes,
 * zero-padded). Unknown messages are counted, not fatal. */
#include <zephyr/kernel.h>
#include <zephyr/device.h>
#include <zephyr/ipc/ipc_service.h>
#include <zephyr/logging/log.h>
#include "g1.h"

LOG_MODULE_REGISTER(g1_netcore, LOG_LEVEL_INF);

static struct ipc_ept ept;
static K_SEM_DEFINE(bound_sem, 0, 1);
static K_SEM_DEFINE(role_req_sem, 0, 1);
static K_SEM_DEFINE(addr_sem, 0, 1);
static bool ready;
uint32_t netcore_other_msgs;

static void bound_cb(void *priv)
{
	k_sem_give(&bound_sem);
}

static void hw_id(const uint8_t *d, size_t len)
{
	switch (d[1]) {
	case 0x02:
		k_sem_give(&role_req_sem);
		break;
	case 0x03:
		if (len >= 8) {
			LOG_INF("net core BD_ADDR %02x:%02x:%02x:%02x:%02x:%02x", d[7], d[6], d[5], d[4], d[3],
				d[2]);
		}
		k_sem_give(&addr_sem);
		break;
	case 0x06:
		LOG_INF("net core build %.*s", (int)MIN(len - 2, 30), (const char *)&d[2]);
		break;
	case 0x07:
		if (len >= 15) {
			peer_net_report(get32(&d[2]), get32(&d[6]), get32(&d[10]), d[14]);
		}
		break;
	case 0x0d:
		if (len >= 3) {
			peer_net_sent(d[2]);
		}
		break;
	default:
		netcore_other_msgs++;
		break;
	}
}

static void received_cb(const void *data, size_t len, void *priv)
{
	const uint8_t *d = data;

	if (len >= 2 && d[0] == 0x01) {
		hw_id(d, len);
	} else if (len >= 33 && (d[0] == 0x04 || d[0] == 0x06)) {
		peer_net_frame(&d[1]);
	} else {
		netcore_other_msgs++;
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
	/* byte 3 = work mode; 1 = ESB quiesced in the net core (stock sends it in its DFU mode) */
	uint8_t mode = IS_ENABLED(CONFIG_G1_NET_ESB_QUIET) ? 1 : 0;
	uint8_t msg[16] = {0x01, 0x00, role, mode, esb_channel, esb_master, esb_slave};
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
	LOG_INF("net core accepted role %u mode %u", role, mode);
	ready = mode == 0 && esb_channel != 0xFF && esb_master != 0 && esb_slave != 0;
	if (ready) {
		peer_start(role == 1);
	}
	return 0;
}

/* a frame for the net core's ESB slot (33 bytes: the sub-service, then the frame) */
int g1_netcore_send(const uint8_t *msg, size_t len)
{
	return ready ? ipc_service_send(&ept, msg, len) : -ENOTCONN;
}
