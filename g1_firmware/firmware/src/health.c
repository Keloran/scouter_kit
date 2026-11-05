/* Watchdogs and health: nRF WDT0 (started in the reset entry, CONFIG_G1_WDT0_CRV) is fed only while every
 * thread that matters is alive; the nPM1300 watchdog (30 s, power cycle) is kicked by the PMIC
 * thread only while health_good(). Fatal errors are recorded in retained RAM, then reboot. */
#include <zephyr/kernel.h>
#include <zephyr/sys/reboot.h>
#include <zephyr/fatal.h>
#include <zephyr/logging/log.h>
#include <zephyr/logging/log_ctrl.h>
#include <zephyr/bluetooth/hci.h>
#include <bootutil/bootutil_public.h>
#include <pm_config.h>
#include <nrf.h>
#include "g1.h"

LOG_MODULE_REGISTER(health, LOG_LEVEL_INF);

#define WDT_KEY 0x6E524635u

atomic_t hb_exec, hb_pmic;
volatile uint32_t bt_ok_ms;
static volatile bool host_ok;
static volatile bool ble_enabled, no_bt_checks;
static volatile uint32_t no_bt_until_ms;
static volatile bool good = true;

/* Runs before Zephyr initialises RAM: registers and stack only. */
void g1_early_wdt(void)
{
	NRF_WDT_Type *w = NRF_WDT0_S;

	if (!(w->RUNSTATUS & 1)) {
		w->CRV = CONFIG_G1_WDT0_CRV;
		w->RREN = 1;
		w->CONFIG = 1;
		w->TASKS_START = 1;
	}
	for (int i = 0; i < 8; i++) {
		if (w->RREN & (1u << i)) {
			w->RR[i] = WDT_KEY;
		}
	}
}

void wdt0_feed(void)
{
	NRF_WDT_Type *w = NRF_WDT0_S;
	uint32_t en = w->RREN;

	for (int i = 0; i < 8; i++) {
		if (en & (1u << i)) {
			w->RR[i] = WDT_KEY;
		}
	}
}

bool health_good(void)
{
	return good;
}

void health_host_ok(void)
{
	host_ok = true;
}

/* Called from main once bt_enable has succeeded. The HCI probe and the BT-liveness health check only
 * run after this: with a dead net core bt_enable is never called, and sending an HCI command would
 * time out and BT_ASSERT-reset the arm (the net-core fallback in main handles that case instead). */
void health_set_ble_up(void)
{
	ble_enabled = true;
	bt_ok_ms = k_uptime_get_32();
}

/* An install is pending when a secondary slot (app or net) has a good MCUboot trailer, written by
 * MCUmgr's image test/confirm or by the net fallback: MCUboot then overwrites the primary at the
 * next boot (and erases that trailer). A trailer that cannot be read counts as pending. */
bool g1_install_pending(void)
{
	static const int slots[] = {PM_MCUBOOT_SECONDARY_ID, PM_MCUBOOT_SECONDARY_1_ID};

	for (int i = 0; i < ARRAY_SIZE(slots); i++) {
		struct boot_swap_state st;

		if (boot_read_swap_state_by_id(slots[i], &st) != 0 || st.magic == BOOT_MAGIC_GOOD) {
			return true;
		}
	}
	return false;
}

/* Every deliberate reboot (SMP reset, host REBOOT, net fallback): into an install the nPM1300
 * watchdog is stopped as stock's DFU mode does; otherwise it is kicked for the next boot. */
void g1_prepare_reboot(void)
{
	if (g1_install_pending()) {
		LOG_WRN("rebooting into an install (WDT0 window %u s)", NRF_WDT0_S->CRV / 32768);
		pmic_watchdog_stop_for_install();
	} else {
		pmic_kick_now();
	}
}

void g1_reboot(enum clean_kind kind, bool clear_counter, bool force_safe)
{
	if (kind != CLEAN_NONE) {
		retained_mark_clean(kind, clear_counter, force_safe);
		g1_prepare_reboot();
	}
	LOG_PANIC();
	sys_reboot(SYS_REBOOT_COLD);
}

void k_sys_fatal_error_handler(unsigned int reason, const z_arch_esf_t *esf)
{
	uint32_t pc = esf ? esf->basic.pc : 0;
	uint32_t lr = esf ? esf->basic.lr : 0;

	retained_record_fault(reason, pc, lr);
	LOG_ERR("fatal error %u pc 0x%08x lr 0x%08x, rebooting", reason, pc, lr);
	LOG_PANIC();
	sys_reboot(SYS_REBOOT_COLD);
	CODE_UNREACHABLE;
}

/* the net-off measurement (diag.c) runs without Bluetooth on purpose, until a deadline: past it the
 * health check reboots into a normal boot, whatever the measurement is doing */
void health_no_bt_checks(uint32_t until_ms)
{
	no_bt_until_ms = until_ms;
	no_bt_checks = true;
}

void health_bt_traffic(void)
{
	bt_ok_ms = k_uptime_get_32();
}

/* BLE liveness: received traffic (protocol or SMP) proves it; after 10 s without traffic an HCI
 * round trip does. The HCI probe is skipped while traffic flows: in the emulator an HCI command
 * sent during a long SMP upload once went unanswered for 10 s, and Zephyr's BT_ASSERT then reset
 * the arm in the middle of the upload. */
static void bt_probe_thread(void *a, void *b, void *c)
{
	for (;;) {
		struct net_buf *rsp = NULL;
		uint32_t ok;

		k_sleep(K_SECONDS(10));
		ok = bt_ok_ms;
		if (!ble_enabled || k_uptime_get_32() - ok < 10000) {
			continue;
		}
		if (bt_hci_cmd_send_sync(BT_HCI_OP_READ_LOCAL_VERSION_INFO, NULL, &rsp) == 0) {
			bt_ok_ms = k_uptime_get_32();
		}
		if (rsp) {
			net_buf_unref(rsp);
		}
	}
}

K_THREAD_DEFINE(bt_probe, 1024, bt_probe_thread, NULL, NULL, NULL, 10, 0, K_TICKS_FOREVER);

enum { HR_EXEC = 1, HR_PMIC = 3, HR_BT = 4, HR_NO_BT = 5, HR_TEST_TIME = 6 };

struct beat {
	atomic_t *hb;
	atomic_val_t last;
	uint32_t seen;
	uint32_t limit_ms;
	uint8_t max_level;
	uint8_t reason;
};

/* A thread that has never beaten gets 30 s to start; after that each must beat within its limit. */
static bool beat_ok(struct beat *b, uint32_t now)
{
	atomic_val_t v = atomic_get(b->hb);

	if (v != b->last) {
		b->last = v;
		b->seen = now;
	}
	if (safe_level() > b->max_level) {
		return true;
	}
	if (v == 0) {
		return now < 30000;
	}
	return now - b->seen <= b->limit_ms;
}

static void health_thread(void *a, void *b, void *c)
{
	struct beat beats[] = {
		{&hb_exec, 0, 0, 3000, 0, HR_EXEC},
		{&hb_pmic, 0, 0, 20000, 1, HR_PMIC},
	};
	bool cleared = false;
	uint32_t last = 0, ahead = 0, jumps = 0;

	for (;;) {
		/* bt_ok_ms before now: traffic in between (a higher-priority thread) may set it later than now */
		uint32_t ok = bt_ok_ms;
		uint32_t now = k_uptime_get_32();
		uint32_t reason = 0;

		for (int i = 0; i < ARRAY_SIZE(beats); i++) {
			if (!beat_ok(&beats[i], now) && !reason) {
				reason = beats[i].reason;
			}
		}
		if (!reason && ble_enabled && now > 40000 && now - ok > 35000) {
			reason = HR_BT;
		}
		if ((int32_t)(bt_ok_ms - now) > 0 && ahead++ < 8) {
			LOG_WRN("health: BT traffic at %u, after this check's %u (%u times)", bt_ok_ms, now, ahead);
		}
		if (last && (now - last > 3000 || (int32_t)(now - last) < 0) && jumps++ < 8) {
			LOG_WRN("health: uptime %u after %u", now, last);
		}
		last = now;
		/* Bluetooth must come up within N s (net-core fallback, netfallback.c); after the one-shot
		 * fallback the arm stays up without BLE instead. */
		if (!reason && !ble_enabled && !no_bt_checks && !retained_net_fallback_done() &&
		    now > CONFIG_G1_NET_FALLBACK_TIMEOUT_S * 1000U) {
			reason = HR_NO_BT;
		}
		if (!reason && no_bt_checks && now > no_bt_until_ms) {
			reason = HR_TEST_TIME;
		}
		if (reason) {
			good = false;
			retained_set_health_reason(reason);
			LOG_ERR("health check %u failed at %u ms, rebooting (BT traffic at %u, now %u)", reason, now, ok,
				bt_ok_ms);
			g1_reboot(CLEAN_NONE, false, false);
		}
		wdt0_feed();
		if (!cleared && host_ok && now > 60000 && now - ok < 20000) {
			cleared = true;
			retained_clear_unhealthy();
			LOG_INF("healthy for 60 s with a host: boot counter cleared");
		}
		k_sleep(K_MSEC(500));
	}
}

K_THREAD_DEFINE(health, 1024, health_thread, NULL, NULL, NULL, 2, 0, K_TICKS_FOREVER);

void health_start(void)
{
	/* the CRV that runs may be an older image's: a running WDT0 keeps it until a non-soft reset */
	LOG_INF("WDT0 %s, CRV %u s", (NRF_WDT0_S->RUNSTATUS & 1) ? "running" : "stopped",
		NRF_WDT0_S->CRV / 32768);
	bt_ok_ms = k_uptime_get_32();
	k_thread_start(health);
	k_thread_start(bt_probe);
}
